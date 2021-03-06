package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	qrterminal "github.com/mdp/qrterminal/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"moul.io/srand"
	"moul.io/u"
	"moul.io/zapconfig"

	"berty.tech/berty/v2/go/pkg/bertymessenger"
)

const (
	staffXConvPrefix = "Berty Staff X "
)

var (
	nodeAddr      = flag.String("addr", "127.0.0.1:9091", "remote 'berty daemon' address")
	displayName   = flag.String("display-name", safeDefaultDisplayName(), "bot's display name")
	staffConvLink = flag.String("staff-conversation-link", "", "link of the staff's conversation to join")
	storePath     = flag.String("store", "./betabot.store", "store file path")
)

func main() {
	if err := betabot(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %+v\n", err)
		os.Exit(1)
	}
}

type Bot struct {
	store struct {
		Convs       []Conversation
		StaffConvPK string
	}
	client      bertymessenger.MessengerServiceClient
	storeIsNew  bool
	storePath   string
	storeMutex  sync.Mutex
	isReplaying bool
	logger      *zap.Logger
}

type Conversation struct {
	ConversationPublicKey string
	ContactPublicKey      string
	ContactDisplayName    string
	Count                 int
	IsOneToOne            bool
}

func betabot() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	flag.Parse()
	rand.Seed(srand.MustSecure())

	// init bot
	var bot Bot

	// init logger
	{
		logger, err := zapconfig.Configurator{}.BuildLogger()
		if err != nil {
			return fmt.Errorf("build zap logger failed: %w", err)
		}
		bot.logger = logger
	}

	// init store
	{
		if u.FileExists(*storePath) {
			data, err := ioutil.ReadFile(*storePath)
			if err != nil {
				return fmt.Errorf("read %q: %w", *storePath, err)
			}

			// parse
			err = json.Unmarshal(data, &bot.store)
			if err != nil {
				return fmt.Errorf("parse %q: %w", *storePath, err)
			}
			bot.isReplaying = true // if the db exists on disk, then we switch the bot to replay mode

			// debug
			bot.logger.Info("store loaded from file",
				zap.String("path", *storePath),
				zap.String("staff-conv-pk", bot.store.StaffConvPK),
			)
			for _, conv := range bot.store.Convs {
				bot.logger.Debug("loaded conv from store", zap.Any("meta", conv))
			}
		} else {
			bot.storeIsNew = true
		}
		bot.storePath = *storePath
	}

	// init messenger gRPC client
	{
		bot.logger.Info("connecting to remote berty messenger node", zap.String("addr", *nodeAddr))
		cc, err := grpc.Dial(*nodeAddr, grpc.WithInsecure())
		if err != nil {
			return fmt.Errorf("connect to remote berty messenger node: %w", err)
		}
		bot.client = bertymessenger.NewMessengerServiceClient(cc)
	}

	// get sharing link and print qr code
	{
		req := &bertymessenger.InstanceShareableBertyID_Request{DisplayName: *displayName}
		res, err := bot.client.InstanceShareableBertyID(ctx, req)
		if err != nil {
			return fmt.Errorf("get instance shareable berty ID failed: %w", err)
		}
		bot.logger.Info("retrieve instance Berty ID", zap.String("link", res.HTMLURL))
		qrterminal.GenerateHalfBlock(res.HTMLURL, qrterminal.L, os.Stdout)
	}

	// join staff conversation
	{
		var (
			noFlagProvided       = *staffConvLink == ""
			alreadyExistsInStore = bot.store.StaffConvPK != ""
			shouldJoin           = !noFlagProvided && !alreadyExistsInStore
		)
		switch {
		case noFlagProvided:
			bot.logger.Warn("missing -staff-conv-link")
			// noop
		case alreadyExistsInStore:
			// noop
			bot.logger.Info("staff conv is already (or should already be) joined")
			// FIXME: or should we join the group again?
		case shouldJoin:
			bot.logger.Info("joining staff conv")
			req := &bertymessenger.ConversationJoin_Request{
				Link: *staffConvLink,
			}
			_, err := bot.client.ConversationJoin(ctx, req)
			if err != nil {
				return fmt.Errorf("conversation join failed: %w", err)
			}

			// store staffConvPk
			link := req.GetLink()
			if link == "" {
				return fmt.Errorf("cannot get link")
			}

			query, method, err := bertymessenger.NormalizeDeepLinkURL(req.Link)
			if err != nil {
				return fmt.Errorf("normalize deeplink failed: %w", err)
			}

			var pdlr *bertymessenger.ParseDeepLink_Reply
			switch method {
			case "/group":
				pdlr, err = bertymessenger.ParseGroupInviteURLQuery(query)
				if err != nil {
					return fmt.Errorf("parse group invite failed: %w", err)
				}
			default:
				return fmt.Errorf("invalid link input")
			}
			bgroup := pdlr.GetBertyGroup()
			gpkb := bgroup.GetGroup().GetPublicKey()
			bot.store.StaffConvPK = base64.RawURLEncoding.EncodeToString(gpkb)
			bot.saveStore()
		}
	}

	// event loop
	var wg sync.WaitGroup
	{
		s, err := bot.client.EventStream(ctx, &bertymessenger.EventStream_Request{})
		if err != nil {
			return fmt.Errorf("failed to listen to EventStream: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handledEvents := 0
			for {
				gme, err := s.Recv()
				if err != nil {
					cancel()
					bot.logger.Error("stream error", zap.Error(err))
					return
				}

				switch {
				case gme.Event.Type == bertymessenger.StreamEvent_TypeListEnd:
					bot.logger.Info("finished replaying logs from the previous sessions", zap.Int("count", handledEvents))
					bot.isReplaying = false
				case !bot.isReplaying:
					// replay is done, let's handle events normally
					wg.Add(1)
					go func() {
						defer wg.Done()
						err := bot.handleEvent(ctx, gme)
						if err != nil {
							bot.logger.Error("handleEvent failed", zap.Error(err))
						}
					}()
				default:
					// bot.logger.Debug("ignoring already handled event", zap.Any("event", gme))
					// replayed events
					// noop
				}
				handledEvents++
			}
		}()
	}

	waitForCtrlC(ctx, cancel)
	wg.Wait()
	return nil
}

func (bot *Bot) handleEvent(ctx context.Context, gme *bertymessenger.EventStream_Reply) error {
	handlers := map[bertymessenger.StreamEvent_Type]func(ctx context.Context, gme *bertymessenger.EventStream_Reply, payload proto.Message) error{
		bertymessenger.StreamEvent_TypeContactUpdated:      bot.handleContactUpdated,
		bertymessenger.StreamEvent_TypeInteractionUpdated:  bot.handleInteractionUpdated,
		bertymessenger.StreamEvent_TypeConversationUpdated: bot.handleConversationUpdated,
	}

	handler, found := handlers[gme.Event.Type]
	if !found {
		bot.logger.Debug("handling event", zap.Any("event", gme))
		return fmt.Errorf("unhandled event type: %q", gme.Event.Type)
	}

	payload, err := gme.Event.UnmarshalPayload()
	if err != nil {
		return fmt.Errorf("unmarshal payload failed: %w", err)
	}
	bot.logger.Info("handling event", zap.Any("event", gme), zap.Any("payload", payload))

	if err := handler(ctx, gme, payload); err != nil {
		return fmt.Errorf("handler %q error: %w", gme.Event.Type, err)
	}

	return nil
}

func (bot *Bot) handleContactUpdated(ctx context.Context, _ *bertymessenger.EventStream_Reply, payload proto.Message) error {
	// auto-accept contact requests
	contact := payload.(*bertymessenger.StreamEvent_ContactUpdated).Contact

	if contact.State == bertymessenger.Contact_IncomingRequest {
		req := &bertymessenger.ContactAccept_Request{PublicKey: contact.PublicKey}
		_, err := bot.client.ContactAccept(ctx, req)
		if err != nil {
			return fmt.Errorf("contact accept failed: %w", err)
		}
	} else if contact.State == bertymessenger.Contact_Accepted {
		// When contact was established, send message and a group invitation
		time.Sleep(2 * time.Second)
		bot.store.Convs = append(bot.store.Convs, Conversation{
			ConversationPublicKey: contact.ConversationPublicKey,
			ContactPublicKey:      contact.PublicKey,
			Count:                 0,
			ContactDisplayName:    contact.DisplayName,
			IsOneToOne:            true,
		})
		bot.saveStore()

		body := `Hey! 🙌 Welcome to the Berty beta version! 🎊
I’m here to help you with the onboarding process.
Let's test out some features together!
Just type 'yes' to let me know you copy that.`
		if err := bot.interactUserMessage(ctx, body, contact.ConversationPublicKey); err != nil {
			return fmt.Errorf("interact user message failed: %w", err)
		}
	}
	return nil
}

func (bot *Bot) handleUserMessageInteractionUpdated(ctx context.Context, _ *bertymessenger.EventStream_Reply, interaction *bertymessenger.Interaction, payload proto.Message) error {
	if interaction.IsMe || interaction.Acknowledged {
		return nil
	}

	var conv *Conversation
	for i := range bot.store.Convs {
		if bot.store.Convs[i].ConversationPublicKey == interaction.ConversationPublicKey {
			conv = &bot.store.Convs[i]
		}
	}
	receivedMessage := payload.(*bertymessenger.AppMessage_UserMessage)
	if conv != nil && conv.IsOneToOne {
		switch {
		case conv.Count == 0 && checkValidationMessage(receivedMessage.GetBody()):
			conv.Count++
			bot.saveStore()
			time.Sleep(1 * time.Second)

			body := `Okay, perfect! 🤙
Would you like me to invite you to a group chat to test multimember conversations?
Type 'yes' to receive it! 💌`
			if err := bot.interactUserMessage(ctx, body, interaction.ConversationPublicKey); err != nil {
				return fmt.Errorf("interact user message failed: %w", err)
			}
		case conv.Count == 1 && checkValidationMessage(receivedMessage.GetBody()):
			conv.Count++
			bot.saveStore()
			time.Sleep(1 * time.Second)

			body := `Okay, I'm inviting you! 🤝
I'll also invite some staff members to join the group!
I’m cool, but humans are sometimes cooler than me… 🤖 ❤️`
			if err := bot.interactUserMessage(ctx, body, interaction.ConversationPublicKey); err != nil {
				return fmt.Errorf("interact user message failed: %w", err)
			}

			// TODO create with real staff group (in this group, betabot auto-reply will be disable)
			time.Sleep(1 * time.Second)
			{
				// create staff conversation
				createdConv, err := bot.client.ConversationCreate(ctx, &bertymessenger.ConversationCreate_Request{
					DisplayName: staffXConvPrefix + conv.ContactDisplayName,
					ContactsToInvite: []string{
						conv.ContactPublicKey,
					},
				})
				if err != nil {
					return fmt.Errorf("conversation create failed: %w", err)
				}
				bot.store.Convs = append(bot.store.Convs, Conversation{
					ConversationPublicKey: createdConv.PublicKey,
					IsOneToOne:            false,
				})
				bot.saveStore()
			}
			time.Sleep(1 * time.Second)

			body = `Okay, done! 👏 👍
Welcome and thanks for joining our community. You're part of the revolution now! 🔥
Type /help when you need info about available test commands! 📖`
			if err := bot.interactUserMessage(ctx, body, interaction.ConversationPublicKey); err != nil {
				return fmt.Errorf("interact user message failed: %w", err)
			}
			bot.logger.Info("scenario finished")
		case conv.Count >= 2 && receivedMessage.GetBody() == "/help":
			body := `In this conversation, you can type all theses commands :
/demo group
/demo demo
/demo share
/demo contact "Here is the QR code of manfred, just add him!"`
			if err := bot.interactUserMessage(ctx, body, interaction.ConversationPublicKey); err != nil {
				return fmt.Errorf("interact user message failed: %w", err)
			}
		default:
			// auto-reply to user's messages
			answer := getRandomReply()
			if err := bot.interactUserMessage(ctx, answer, interaction.ConversationPublicKey); err != nil {
				return fmt.Errorf("interact user message failed: %w", err)
			}
		}
	}
	return nil
}

func (bot *Bot) handleGroupInvitationInteractionUpdated(ctx context.Context, _ *bertymessenger.EventStream_Reply, interaction *bertymessenger.Interaction, payload proto.Message) error {
	if !interaction.IsMe {
		// auto-accept invitations to group
		receivedInvitation := payload.(*bertymessenger.AppMessage_GroupInvitation)
		_, err := bot.client.ConversationJoin(ctx, &bertymessenger.ConversationJoin_Request{
			Link: receivedInvitation.GetLink(),
		})
		if err != nil {
			return fmt.Errorf("conversation join failed: %w", err)
		}
	}
	return nil
}

func (bot *Bot) handleInteractionUpdated(ctx context.Context, gme *bertymessenger.EventStream_Reply, payload proto.Message) error {
	interaction := payload.(*bertymessenger.StreamEvent_InteractionUpdated).Interaction

	handlers := map[bertymessenger.AppMessage_Type]func(ctx context.Context, gme *bertymessenger.EventStream_Reply, interaction *bertymessenger.Interaction, payload proto.Message) error{
		bertymessenger.AppMessage_TypeUserMessage:     bot.handleUserMessageInteractionUpdated,
		bertymessenger.AppMessage_TypeGroupInvitation: bot.handleGroupInvitationInteractionUpdated,
	}
	handler, found := handlers[interaction.Type]
	if !found {
		return fmt.Errorf("unsupported interaction type: %q", interaction.Type)
	}

	payload, err := interaction.UnmarshalPayload()
	if err != nil {
		return fmt.Errorf("unmarshal interaction payload failed: %w", err)
	}
	bot.logger.Debug("handling interaction", zap.Any("payload", payload))

	if err := handler(ctx, gme, interaction, payload); err != nil {
		return fmt.Errorf("handle %q failed: %w", interaction.Type, err)
	}

	return nil
}

func (bot *Bot) handleConversationUpdated(ctx context.Context, _ *bertymessenger.EventStream_Reply, payload proto.Message) error {
	// send to multimember staff conv that this user join us on Berty with the link of the group
	conversation := payload.(*bertymessenger.StreamEvent_ConversationUpdated).Conversation
	if bot.store.StaffConvPK != "" && strings.HasPrefix(conversation.GetDisplayName(), staffXConvPrefix) {
		userName := strings.TrimPrefix(conversation.GetDisplayName(), staffXConvPrefix)
		body := fmt.Sprintf(
			`Hi guys, we have a new user in our community! 🥳
Say hello to %s! 👍
For the moment i can't send a group invitation so i share the link of the conversation here: %s`,
			userName,
			conversation.GetLink(),
		)
		if err := bot.interactUserMessage(ctx, body, bot.store.StaffConvPK); err != nil {
			return fmt.Errorf("interact user message failed: %w", err)
		}

		// TODO send the group invitation, maybe the group invitation was broken so for the moment i sent the link in a message

		// time.Sleep(2 * time.Second)
		// {
		// 	groupInvitation, err := proto.Marshal(&bertymessenger.AppMessage_GroupInvitation{
		// 		Link: conversation.GetLink(),
		// 	})
		// 	if err != nil {
		// 		return err
		// 	}
		// 	_, err = bot.client.Interact(ctx, &bertymessenger.Interact_Request{
		// 		Type:                  bertymessenger.AppMessage_TypeGroupInvitation,
		// 		Payload:               groupInvitation,
		// 		ConversationPublicKey: staffConvPk,
		// 	})
		// 	if err != nil {
		// 		return err
		// 	}
		// }
	}
	return nil
}

// internal stuff

func (bot *Bot) interactUserMessage(ctx context.Context, body string, conversationPK string) error {
	userMessage, err := proto.Marshal(&bertymessenger.AppMessage_UserMessage{Body: body})
	if err != nil {
		return fmt.Errorf("marshal user message failed: %w", err)
	}
	_, err = bot.client.Interact(ctx, &bertymessenger.Interact_Request{
		Type:                  bertymessenger.AppMessage_TypeUserMessage,
		Payload:               userMessage,
		ConversationPublicKey: conversationPK,
	})
	if err != nil {
		return fmt.Errorf("interact failed: %w", err)
	}
	return nil
}

func (bot *Bot) saveStore() {
	bot.storeMutex.Lock()
	defer bot.storeMutex.Unlock()

	// marshal
	data, err := json.MarshalIndent(bot.store, "", "  ")
	if err != nil {
		panic(fmt.Errorf("marshal: %w", err))
	}

	// write file
	if err := ioutil.WriteFile(bot.storePath, data, 0o600); err != nil {
		panic(fmt.Errorf("write store file: %w", err))
	}
}

func checkValidationMessage(s string) bool {
	switch strings.ToLower(s) {
	case "y", "yes", "yes!":
		return true
	default:
		return false
	}
}

func waitForCtrlC(ctx context.Context, cancel context.CancelFunc) {
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt)

	select {
	case <-signalChannel:
		cancel()
	case <-ctx.Done():
	}
}

func safeDefaultDisplayName() string {
	var name string
	current, err := user.Current()
	if err == nil {
		name = current.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" {
		name = "Anonymous4242"
	}
	return fmt.Sprintf("%s (bot)", name)
}

func getRandomReply() string {
	available := []string{
		"Welcome to the beta!",
		"Hello! Welcome to Berty!",
		"Hey, I hope you're feeling well here!",
		"Hi, I'm here for you at anytime for tests!",
		"Hello dude!",
		"Hello :)",
		"Ow, I like to receive test messages <3",
		"What's up ?",
		"How r u ?",
		"Hello, 1-2, 1-2, check, check?!",
		"Do you copy ?",
		"If you say ping, I'll say pong.",
		"I'm faster than you at sending message :)",
		"One day, bots will rules the world. Or not.",
		"You're so cute.",
		"I like discuss with you, I feel more and more clever.",
		"I'm so happy to chat with you.",
		"I could chat with you all day long.",
		"Yes darling ? Can I help you ?",
		"OK, copy that.",
		"OK, I understand.",
		"Hmmm, Hmmmm. One more time ?",
		"I think you're the most clever human I know.",
		"I missed you babe.",
		"OK, don't send me nudes, I'm a bot dude.",
		"Come on, let's party.",
		"May we have a chat about our love relationship future ?",
		"That's cool. I copy.",
	}
	return available[rand.Intn(len(available))] // nolint:gosec // absolutely no importance in this case
}
