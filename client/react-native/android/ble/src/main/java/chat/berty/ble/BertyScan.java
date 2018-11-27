package chat.berty.ble;

import android.annotation.SuppressLint;
import android.annotation.TargetApi;
import android.bluetooth.le.ScanCallback;
import android.bluetooth.le.ScanFilter;
import android.bluetooth.le.ScanResult;
import android.bluetooth.le.ScanSettings;
import android.os.Build;
import android.os.ParcelUuid;
import android.util.Log;

import java.util.List;

import static chat.berty.ble.BertyConstants.SERVICE_UUID;

@SuppressLint("LongLogTag")
@TargetApi(Build.VERSION_CODES.LOLLIPOP)
public class BertyScan extends ScanCallback {
    private static final String TAG = "chat.berty.ble.BertyScan";


    public static ScanSettings createScanSetting() {
        ScanSettings settings = new ScanSettings.Builder()
                    .setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY)
                    .build();

        return settings;
    }

    public static ScanFilter makeFilter() {
        ParcelUuid pUuid = new ParcelUuid(SERVICE_UUID);
        ScanFilter filter = new ScanFilter.Builder()
                .setServiceUuid(pUuid)
                .build();

        return filter;
    }

    public BertyScan() {
        super();
        Thread.currentThread().setName("BertyScan");
    }

    /**
     * Callback when a BLE advertisement has been found.
     *
     * @param callbackType Determines how this callback was triggered. Could be one of
     *                     {@link ScanSettings#CALLBACK_TYPE_ALL_MATCHES},
     *                     {@link ScanSettings#CALLBACK_TYPE_FIRST_MATCH} or
     *                     {@link ScanSettings#CALLBACK_TYPE_MATCH_LOST}
     * @param result       A Bluetooth LE scan result.
     */
    @Override
    public void onScanResult(int callbackType, ScanResult result) {
        parseResult(result);
        super.onScanResult(callbackType, result);
    }

    /**
     * Callback when batch results are delivered.
     *
     * @param results List of scan results that are previously scanned.
     */
    @Override
    public void onBatchScanResults(List<ScanResult> results) {
        for (ScanResult result:results) {
            parseResult(result);
        }
        super.onBatchScanResults(results);
    }

    /**
     * Callback when scan could not be started.
     *
     * @param errorCode Error code (one of SCAN_FAILED_*) for scan failure.
     */
    @Override
    public void onScanFailed(int errorCode) {
        String errorString;

        switch(errorCode) {
            case SCAN_FAILED_ALREADY_STARTED: errorString = "SCAN_FAILED_ALREADY_STARTED";
                break;

            case SCAN_FAILED_APPLICATION_REGISTRATION_FAILED: errorString = "SCAN_FAILED_APPLICATION_REGISTRATION_FAILED";
                break;

            case SCAN_FAILED_INTERNAL_ERROR: errorString = "SCAN_FAILED_INTERNAL_ERROR";
                break;

            case SCAN_FAILED_FEATURE_UNSUPPORTED: errorString = "SCAN_FAILED_FEATURE_UNSUPPORTED";
                break;

            default: errorString = "UNKNOW FAIL";
                break;
        }
        Log.e(TAG, "error scanning " + errorString);
        super.onScanFailed(errorCode);
    }

    public void parseResult(ScanResult result) {
        Log.e(TAG, "new result");
//        BluetoothDevice device = result.getDevice();
//        String addr = device.getAddress();
//        synchronized (bertyDevices) {
//            if (!bertyDevices.containsKey(addr)) {
//                Log.e(TAG, "SCANNNING RESULT mGattCallback " + mGattCallback);
//                initGattCallback();
//                BluetoothManager bluetoothManager = (BluetoothManager) mContext.getSystemService(Context.BLUETOOTH_SERVICE);
//                List<BluetoothDevice> devices = bluetoothManager.getConnectedDevices(GATT);
//                for(BluetoothDevice devicee : devices) {
//                    Log.e(TAG, "test " + devicee.getAddress());
//
//                }
//                try {
//                    Log.e(TAG, "CONN SUCESsssS");
//                    BluetoothGatt gatt = device.connectGatt(mContext, false, mGattCallback, BluetoothDevice.TRANSPORT_LE);
//
//                    gatt.connect();
//                    Log.e(TAG, "CONN SUCESS");
//                    BertyDevice bDevice = new BertyDevice(device, gatt, addr);
////                                runDiscoAndMtu(gatt);
//                    bertyDevices.put(addr, bDevice);
//                    gatt.requestMtu(512);
////                                gatt.discoverServices();
//                    Log.e(TAG, "PU SUCESS");
//                    for (BluetoothGattService svc : gatt.getServices()) {
//                        Log.e(TAG, "PU SUCESS "+ svc.getUuid().toString());
//                    }
//                    Log.e(TAG, "PU SUCESS2");
//                } catch (Exception e) {
//
//                    Log.e(TAG, "GATT FAIL " + e);
//
//                }
//            }
//        }
    }
}
