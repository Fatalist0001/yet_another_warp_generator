package org.fatalist0001.warpgen_gui

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.LocalSocket
import android.net.LocalSocketAddress
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.util.Log
import io.flutter.plugin.common.EventChannel
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean

/**
 * VpnService: создаёт TUN, запускает Go-пайплайн и передаёт ему fd.
 *
 * Приложение исключено из VPN через addDisallowedApplication: исходящие
 * сокеты Go-процесса идут напрямую, поэтому защищать каждый fd через
 * VpnService.protect не нужно и петля «VPN → SOCKS5 → VPN» не возникает.
 * Другие приложения при этом получают весь трафик через TUN.
 */
class WarpgenVpnService : VpnService() {
    companion object {
        const val ACTION_START = "org.fatalist0001.warpgen_gui.START"
        const val ACTION_STOP = "org.fatalist0001.warpgen_gui.STOP"
        const val ACTION_FORCE_STOP = "org.fatalist0001.warpgen_gui.FORCE_STOP"
        const val EXTRA_ARGS = "args"

        private const val TAG = "warpgen"
        private const val CHANNEL_ID = "warpgen-vpn"
        private const val NOTIFICATION_ID = 0x5750
        private const val TUN_MTU = 1400
        private const val TUN_ADDRESS = "198.18.0.1"
        private const val TUN_PREFIX = 15
        private const val TUN_DNS = "198.18.0.2"

        @Volatile
        var events: EventChannel.EventSink? = null

        private val mainHandler = Handler(Looper.getMainLooper())

        fun emit(event: Map<String, Any?>) {
            mainHandler.post {
                try {
                    events?.success(event)
                } catch (error: IllegalStateException) {
                    Log.w(TAG, "event sink is gone", error)
                }
            }
        }
    }

    private var tun: ParcelFileDescriptor? = null
    private var process: Process? = null
    private var socketPath: String? = null
    private val stopping = AtomicBoolean(false)

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> {
                startForegroundCompat()
                val args = intent.getStringArrayListExtra(EXTRA_ARGS) ?: emptyList()
                startVpn(args)
            }

            ACTION_STOP -> stopVpn(force = false)
            ACTION_FORCE_STOP -> stopVpn(force = true)
            else -> stopVpn(force = true)
        }
        return START_NOT_STICKY
    }

    override fun onRevoke() {
        stopVpn(force = false)
        super.onRevoke()
    }

    override fun onDestroy() {
        stopVpn(force = true)
        super.onDestroy()
    }

    private fun startVpn(args: List<String>) {
        if (process != null) {
            emit(mapOf("type" to "error", "message" to "VPN уже запущен"))
            return
        }
        // После остановки сервис может получить ACTION_START повторно, не
        // пересоздаваясь: снимаем флаг, иначе cleanup() нового процесса
        // решит, что остановка уже идёт, и не закроет TUN.
        stopping.set(false)

        val builder = Builder()
            .setSession("Товарищ майор")
            .setMtu(TUN_MTU)
            .addAddress(TUN_ADDRESS, TUN_PREFIX)
            .addRoute("0.0.0.0", 0)
            .addDnsServer(TUN_DNS)
            .setBlocking(true)

        // IPv6 уводим в TUN и там отбрасываем: иначе трафик утечёт мимо VPN.
        try {
            builder.addAddress("fd00::1", 128)
            builder.addRoute("::", 0)
        } catch (error: IllegalArgumentException) {
            Log.w(TAG, "IPv6 route is not available", error)
        }

        // Самому приложению VPN не нужен: Go-процесс ходит к WARP напрямую.
        try {
            builder.addDisallowedApplication(packageName)
        } catch (error: Exception) {
            emit(
                mapOf(
                    "type" to "error",
                    "message" to "не удалось исключить приложение из VPN: $error",
                ),
            )
            stopSelf()
            return
        }

        val descriptor = try {
            builder.establish()
        } catch (error: Exception) {
            null
        }
        if (descriptor == null) {
            emit(mapOf("type" to "error", "message" to "VpnService не создал TUN"))
            stopSelf()
            return
        }
        tun = descriptor

        val binary = File(applicationInfo.nativeLibraryDir, "libwarpgen.so")
        if (!binary.exists()) {
            emit(
                mapOf(
                    "type" to "error",
                    "message" to "в APK нет libwarpgen.so: ${binary.absolutePath}",
                ),
            )
            stopVpn(force = true)
            return
        }
        binary.setExecutable(true)
        val warpPlus = File(applicationInfo.nativeLibraryDir, "libwarp-plus.so")
        if (warpPlus.exists()) warpPlus.setExecutable(true)

        val socket = File(cacheDir, "tun-${System.currentTimeMillis()}.sock")
        socketPath = socket.absolutePath

        val command = mutableListOf(binary.absolutePath)
        command.addAll(args)
        if (!command.contains("--tun")) command.add("--tun")
        command.add("--tun-fd-socket")
        command.add(socket.absolutePath)

        val builderProcess = ProcessBuilder(command)
            .redirectErrorStream(false)
        builderProcess.environment()["HOME"] = filesDir.absolutePath
        builderProcess.environment()["TMPDIR"] = cacheDir.absolutePath
        builderProcess.environment()["WARPGEN_ACCOUNT"] =
            File(filesDir, ".warpgen/warpgen-account.json").absolutePath

        val started = try {
            builderProcess.start()
        } catch (error: Exception) {
            emit(mapOf("type" to "error", "message" to "не удалось запустить CLI: $error"))
            stopVpn(force = true)
            return
        }
        process = started

        Thread({ pump(started.inputStream, "stdout") }, "warpgen-stdout").start()
        Thread({ pump(started.errorStream, "stderr") }, "warpgen-stderr").start()
        Thread({ waitProcess(started) }, "warpgen-wait").start()
        Thread({ sendTunFd(descriptor, socket.absolutePath, started) }, "warpgen-tun").start()
    }

    private fun pump(stream: java.io.InputStream, type: String) {
        try {
            stream.bufferedReader().useLines { lines ->
                lines.forEach { line ->
                    Log.d(TAG, "$type: $line")
                    emit(mapOf("type" to type, "line" to line))
                }
            }
        } catch (error: java.io.IOException) {
            // stopVpn() закрывает поток дочернего процесса из другого потока:
            // readLine() получает InterruptedIOException. Это штатная
            // остановка, а не сбой — иначе Android убивает всё приложение.
            Log.d(TAG, "$type: поток закрыт при остановке: ${error.message}")
        }
    }

    private fun waitProcess(started: Process) {
        val code = try {
            started.waitFor()
        } catch (error: InterruptedException) {
            Thread.currentThread().interrupt()
            1
        }
        emit(mapOf("type" to "exit", "code" to code))
        cleanup()
    }

    /**
     * Ждёт, пока Go создаст unix-сокет, подключается и отправляет fd через
     * SCM_RIGHTS. Kotlin не может передать произвольный fd дочернему процессу
     * иначе, а TUN без root доступен только через VpnService.
     */
    private fun sendTunFd(
        descriptor: ParcelFileDescriptor,
        path: String,
        started: Process,
    ) {
        val deadline = System.currentTimeMillis() + 10_000
        var socket: LocalSocket? = null
        while (System.currentTimeMillis() < deadline) {
            if (!started.isAlive) break
            try {
                val candidate = LocalSocket()
                candidate.connect(
                    LocalSocketAddress(path, LocalSocketAddress.Namespace.FILESYSTEM),
                )
                socket = candidate
                break
            } catch (_: Exception) {
                Thread.sleep(50)
            }
        }
        if (socket == null) {
            emit(
                mapOf(
                    "type" to "error",
                    "message" to "CLI не открыл unix-сокет для fd TUN",
                ),
            )
            started.destroy()
            return
        }
        try {
            socket.setFileDescriptorsForSend(arrayOf(descriptor.fileDescriptor))
            socket.outputStream.write(1)
            socket.outputStream.flush()
        } catch (error: Exception) {
            emit(mapOf("type" to "error", "message" to "передача fd TUN: $error"))
            started.destroy()
        } finally {
            try {
                socket.close()
            } catch (_: Exception) {
                // сокет уже закрыт
            }
        }
    }

    private fun stopVpn(force: Boolean) {
        if (!stopping.compareAndSet(false, true)) return
        val current = process
        process = null
        if (current != null) {
            if (force) {
                current.destroyForcibly()
            } else {
                current.destroy()
                mainHandler.postDelayed({
                    if (current.isAlive) current.destroyForcibly()
                }, 5_000)
            }
        }
        closeTun()
        stopForegroundCompat()
        stopSelf()
    }

    private fun cleanup() {
        if (!stopping.compareAndSet(false, true)) return
        process = null
        closeTun()
        stopForegroundCompat()
        stopSelf()
    }

    private fun closeTun() {
        try {
            tun?.close()
        } catch (_: Exception) {
            // уже закрыт
        }
        tun = null
        val path = socketPath
        socketPath = null
        if (path != null) {
            try {
                File(path).delete()
            } catch (_: Exception) {
                // файла уже нет
            }
        }
    }

    private fun startForegroundCompat() {
        val manager = getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "VPN",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "Состояние туннеля «Товарищ майор»"
                setShowBadge(false)
            }
            manager.createNotificationChannel(channel)
        }

        val pending = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }
        val notification = builder
            .setContentTitle("Товарищ майор")
            .setContentText("VPN подключён")
            .setSmallIcon(R.mipmap.ic_launcher)
            .setContentIntent(pending)
            .setOngoing(true)
            .build()

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(Service.STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }
}
