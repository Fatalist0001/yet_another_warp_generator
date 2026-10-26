package org.fatalist0001.warpgen_gui

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel
import java.io.File

/**
 * Мост Flutter ↔ Android.
 *
 * Короткие команды CLI GUI запускает сам через dart:io. Для `run --tun`
 * Kotlin создаёт VpnService, запускает упакованный libwarpgen.so и передаёт
 * ему fd TUN по unix-сокету.
 */
class MainActivity : FlutterActivity() {
    companion object {
        private const val CHANNEL = "warpgen/android"
        private const val EVENTS = "warpgen/events"
        private const val VPN_REQUEST = 0x5750
        private const val NOTIFICATIONS_REQUEST = 0x5751
    }

    private var pendingArgs: List<String>? = null
    private var pendingResult: MethodChannel.Result? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        requestNotificationPermission()
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val messenger = flutterEngine.dartExecutor.binaryMessenger

        MethodChannel(messenger, CHANNEL).setMethodCallHandler { call, result ->
            when (call.method) {
                "getPaths" -> {
                    prepareBinaries()
                    result.success(
                        mapOf(
                            "nativeLibraryDir" to applicationInfo.nativeLibraryDir,
                            "filesDir" to filesDir.absolutePath,
                            "cacheDir" to cacheDir.absolutePath,
                        ),
                    )
                }

                "startVpn" -> {
                    val args = call.argument<List<String>>("args") ?: emptyList()
                    startVpn(args, result)
                }

                "stopVpn" -> {
                    sendServiceAction(WarpgenVpnService.ACTION_STOP)
                    result.success(null)
                }

                "forceStopVpn" -> {
                    sendServiceAction(WarpgenVpnService.ACTION_FORCE_STOP)
                    result.success(null)
                }

                else -> result.notImplemented()
            }
        }

        EventChannel(messenger, EVENTS).setStreamHandler(
            object : EventChannel.StreamHandler {
                override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
                    WarpgenVpnService.events = events
                }

                override fun onCancel(arguments: Any?) {
                    WarpgenVpnService.events = null
                }
            },
        )
    }

    /**
     * nativeLibraryDir отдаёт .so с правами без execute, а GUI запускает
     * libwarpgen.so и libwarp-plus.so как обычные процессы. chmod делаем до
     * первого `register`/`dns-presets`/`services`, иначе ProcessBuilder
     * получит Permission denied.
     */
    private fun prepareBinaries() {
        for (name in listOf("libwarpgen.so", "libwarp-plus.so")) {
            val file = File(applicationInfo.nativeLibraryDir, name)
            if (file.exists()) file.setExecutable(true)
        }
    }

    private fun startVpn(args: List<String>, result: MethodChannel.Result) {
        val permission = VpnService.prepare(this)
        if (permission == null) {
            startVpnService(args)
            result.success(null)
            return
        }
        pendingArgs = args
        pendingResult = result
        @Suppress("DEPRECATION")
        startActivityForResult(permission, VPN_REQUEST)
    }

    private fun startVpnService(args: List<String>) {
        val intent = Intent(this, WarpgenVpnService::class.java).apply {
            action = WarpgenVpnService.ACTION_START
            putStringArrayListExtra(WarpgenVpnService.EXTRA_ARGS, ArrayList(args))
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent)
        } else {
            startService(intent)
        }
    }

    private fun sendServiceAction(action: String) {
        val intent = Intent(this, WarpgenVpnService::class.java).apply {
            this.action = action
        }
        startService(intent)
    }

    @Deprecated("FlutterActivity не наследует ComponentActivity")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != VPN_REQUEST) return
        val args = pendingArgs
        val result = pendingResult
        pendingArgs = null
        pendingResult = null
        if (resultCode == Activity.RESULT_OK && args != null) {
            startVpnService(args)
            result?.success(null)
        } else {
            result?.error("vpn_permission", "Пользователь не разрешил VPN", null)
        }
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        ) {
            return
        }
        requestPermissions(
            arrayOf(Manifest.permission.POST_NOTIFICATIONS),
            NOTIFICATIONS_REQUEST,
        )
    }
}
