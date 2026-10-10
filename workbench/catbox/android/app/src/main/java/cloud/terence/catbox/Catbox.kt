package cloud.terence.catbox

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.provider.MediaStore
import org.json.JSONObject
import java.io.File
import java.util.concurrent.atomic.AtomicReference

/**
 * The bridge to the catbox binary (libcatbox.so in nativeLibraryDir):
 * one-shot execs for join/status/send/recv and one long-lived process
 * for recv --listen. The app is a second CLI, not a daemon fork.
 */
object Catbox {

    /** The exec'd listener, if the toggle is on. */
    private val listener = AtomicReference<Process?>(null)

    /** The exec'd one-shot action (recv/send/...), for cancel. */
    private val current = AtomicReference<Process?>(null)

    fun bin(context: Context): String =
        File(context.applicationInfo.nativeLibraryDir, "libcatbox.so").absolutePath

    /** HOME for the child: the identity lives in HOME/.config/catbox. */
    private fun home(context: Context): File = context.filesDir

    /** The inbox, passed via --dir: app-specific external storage. */
    fun inbox(context: Context): File {
        val dir = context.getExternalFilesDir(null) ?: context.filesDir
        return File(dir, "catbox")
    }

    private fun builder(context: Context, vararg args: String): ProcessBuilder {
        val pb = ProcessBuilder(bin(context), *args).redirectErrorStream(true)
        pb.environment()["HOME"] = home(context).absolutePath
        pb.environment()["CATBOX_LOG_TEXT"] = "1"
        return pb
    }

    /**
     * Runs one catbox command; every output line goes to onLine as it
     * arrives. Returns the exit code. Must run off the main thread.
     */
    fun run(context: Context, onLine: (String) -> Unit, vararg args: String): Int {
        val p = builder(context, *args).start()
        current.set(p)

        // The stream can break mid-read (process killed, network
        // gone); the pane already has what was printed, the exit code
        // still tells the truth.
        try {
            p.inputStream.bufferedReader().forEachLine(onLine)
        } catch (_: java.io.IOException) {
        }
        val code = p.waitFor()
        current.compareAndSet(p, null)
        return code
    }

    /**
     * Kills the running one-shot action: a pull or send aborts
     * mid-flight. A pulled item was never acked, so it stays parked
     * at the stash.
     */
    fun cancelAction() {
        current.getAndSet(null)?.destroy()
    }

    /** The status --json answer, or null when the command fails. */
    data class Status(val name: String, val waiting: List<Wait>, val members: List<Member>) {
        data class Wait(val id: String, val fn: String, val from: String, val plain: Long)
        data class Member(val name: String, val listening: Boolean)
    }

    fun status(context: Context, onLine: (String) -> Unit): Status? {
        val p = builder(context, "status", "--json").start()
        val out = StringBuilder()

        try {
            p.inputStream.bufferedReader().forEachLine { line ->
                if (!line.startsWith("time=")) onLine(line) // log lines go to the pane
                out.appendLine(line)
            }
        } catch (_: java.io.IOException) {
        }

        if (p.waitFor() != 0) return null
        return parseStatus(out.toString())
    }

    /** The `catbox version` output, "dev" or the baked build stamp. */
    fun version(context: Context): String {
        val p = builder(context, "version").start()
        val out = StringBuilder()

        try {
            p.inputStream.bufferedReader().forEachLine {
                if (!it.startsWith("time=")) out.appendLine(it) // log lines stay out
            }
        } catch (_: java.io.IOException) {
        }

        p.waitFor()
        return out.toString().trim().ifEmpty { "catbox unknown" }
    }

    private fun parseStatus(text: String): Status? {
        // The JSON object is one compact line (the Go side guarantees
        // it); log lines may precede it. Parse failures return null
        // rather than crash the app.
        for (raw in text.lineSequence().toList().asReversed()) {
            val line = raw.trim()
            if (!line.startsWith("{")) continue

            return try {
                val o = JSONObject(line)
                val waiting = o.optJSONArray("waiting") ?: org.json.JSONArray()
                val members = o.optJSONArray("members") ?: org.json.JSONArray()
                Status(
                    o.getString("name"),
                    (0 until waiting.length()).map { i ->
                        val w = waiting.getJSONObject(i)
                        Status.Wait(
                            w.optString("id", ""),
                            w.optString("fn", "?"),
                            w.optString("from", "?"),
                            w.optLong("plain", 0),
                        )
                    },
                    (0 until members.length()).map { i ->
                        val m = members.getJSONObject(i)
                        Status.Member(m.getString("name"), m.optString("addr", "").isNotEmpty())
                    },
                )
            } catch (e: org.json.JSONException) {
                null
            }
        }
        return null
    }

    /** True when the failure output shows there is no identity yet. */
    fun noIdentity(lines: List<String>): Boolean =
        lines.any { it.contains("run join with --name") }

    /**
     * Member names from the roster cache the binary maintains —
     * available even when the stash is unreachable.
     */
    fun cachedMembers(context: Context): List<String> = readCache(context, "roster.json")?.let { text ->
        try {
            val arr = org.json.JSONArray(text)
            (0 until arr.length()).map { arr.getJSONObject(it).getString("name") }
        } catch (_: Exception) {
            null
        }
    } ?: emptyList()

    /** This device's member name from its identity. */
    fun cachedName(context: Context): String? = readCache(context, "identity.json")?.let { text ->
        try {
            org.json.JSONObject(text).getString("name")
        } catch (_: Exception) {
            null
        }
    }

    /** The invite token for enrolling another device; the binary's
     *  error (no identity, not an admin, stash unreachable) when it
     *  fails — the caller tells them apart by shape. */
    fun invite(context: Context): String {
        val p = builder(context, "invite").start()
        val out = StringBuilder()
        val err = StringBuilder()
        try {
            p.inputStream.bufferedReader().forEachLine { out.appendLine(it) }
            p.errorStream.bufferedReader().forEachLine { err.appendLine(it) }
        } catch (_: java.io.IOException) {
        }
        if (p.waitFor() != 0) {
            return err.toString().lineSequence().firstOrNull { it.isNotBlank() } ?: "invite failed"
        }
        return out.toString().lineSequence().firstOrNull { it.isNotBlank() } ?: "invite failed"
    }

    private fun readCache(context: Context, name: String): String? = try {
        File(home(context), ".config/catbox/$name").takeIf { it.isFile }?.readText()
    } catch (_: Exception) {
        null
    }

    /**
     * A file published to Download/Catbox, where the user (and other
     * apps) can actually manage it. Date is epoch seconds.
     */
    data class Published(val name: String, val size: Long, val date: Long, val uri: Uri)

    /**
     * Moves finished files from the app-private staging inbox into
     * Download/Catbox via MediaStore. Android/data is locked glass:
     * nothing outside this app can delete from it, so the public
     * folder is the real destination. Serialized: two publishers
     * racing the same staged file would insert it twice.
     */
    fun publish(context: Context) {
        synchronized(this) {
            val staged = inbox(context).listFiles()?.filter { !it.name.startsWith(".part-") } ?: return
            if (staged.isEmpty()) return
            val resolver = context.contentResolver
            for (f in staged) {
                val values = ContentValues().apply {
                    put(MediaStore.Downloads.DISPLAY_NAME, f.name)
                    put(MediaStore.Downloads.MIME_TYPE, mimeOf(f.name))
                    put(MediaStore.Downloads.RELATIVE_PATH, "Download/Catbox")
                    put(MediaStore.Downloads.IS_PENDING, 1)
                }
                val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) ?: continue
                resolver.openOutputStream(uri)?.use { out ->
                    f.inputStream().use { it.copyTo(out) }
                }
                val done = ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }
                resolver.update(uri, done, null, null)
                f.delete()
            }
        }
    }

    /** Files currently in Download/Catbox, newest first. */
    fun published(context: Context): List<Published> {
        val resolver = context.contentResolver
        val out = mutableListOf<Published>()
        resolver.query(
            MediaStore.Downloads.EXTERNAL_CONTENT_URI,
            arrayOf(
                MediaStore.Downloads.DISPLAY_NAME,
                MediaStore.Downloads.SIZE,
                MediaStore.Downloads._ID,
                MediaStore.Downloads.DATE_ADDED,
            ),
            "${MediaStore.Downloads.RELATIVE_PATH} = ?",
            arrayOf("Download/Catbox/"),
            // Stable, newest first: new files prepend, nothing shuffles.
            "${MediaStore.Downloads.DATE_ADDED} DESC, ${MediaStore.Downloads._ID} DESC",
        )?.use { c ->
            while (c.moveToNext()) {
                val id = c.getLong(2)
                out.add(
                    Published(
                        c.getString(0),
                        c.getLong(1),
                        c.getLong(3),
                        Uri.withAppendedPath(MediaStore.Downloads.EXTERNAL_CONTENT_URI, id.toString()),
                    ),
                )
            }
        }
        return out
    }

    fun mimeOf(name: String): String = when {
        name.endsWith(".apk") -> "application/vnd.android.package-archive"
        name.endsWith(".jpg", true) || name.endsWith(".png", true) -> "image/*"
        name.endsWith(".mp4", true) -> "video/mp4"
        name.endsWith(".pdf", true) -> "application/pdf"
        else -> "application/octet-stream"
    }

    /**
     * Starts recv --listen as a long-lived child (killed by
     * stopListener). Its server engine (identity key) never conflicts
     * with one-shot client ops (dial key).
     */
    fun startListener(context: Context, onLine: (String) -> Unit) {
        if (listener.get() != null) return
        val p = builder(
            context,
            "recv", "--dir", inbox(context).absolutePath, "--listen",
        ).start()
        listener.set(p)
        Thread {
            // Stopping the listener closes this stream mid-read; that
            // is the normal exit, not a crash.
            try {
                p.inputStream.bufferedReader().forEachLine(onLine)
            } catch (_: java.io.IOException) {
            }
        }.start()
    }

    fun stopListener() {
        listener.getAndSet(null)?.destroy()
    }

    /**
     * Deletes every resume partial in the staging inbox. Only safe
     * while no receiver can be writing: the listener must be stopped
     * and no pull running — the cancel paths guarantee both.
     */
    fun sweepPartials(context: Context) {
        inbox(context).listFiles()?.filter { it.name.startsWith(".part-") }?.forEach { it.delete() }
    }
}
