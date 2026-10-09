package cloud.terence.catbox

import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.ExitToApp
import androidx.compose.material.icons.outlined.Close
import androidx.compose.material.icons.outlined.ContentPaste
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Download
import androidx.compose.material.icons.outlined.MoreVert
import androidx.compose.material.icons.outlined.Refresh
import androidx.compose.material.icons.outlined.Share
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            CatboxTheme { CatboxApp() }
        }
    }
}

/** A file in flight, announced by a child before the bytes. A pull
 * transforms its own waiting row; a direct receive never parks and
 * loads in the received zone instead. Path updates per tick: a
 * transfer can upgrade DERP to direct. */
data class LiveRecv(val from: String, val file: String, val detail: String, val direct: Boolean, val path: String? = null)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CatboxApp() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val status = remember { mutableStateOf<Catbox.Status?>(null) }
    val joined = remember { mutableStateOf<Boolean?>(null) } // null = checking
    val busy = remember { mutableStateOf(false) } // an action (receive/send) is running
    val progress = remember { mutableStateOf<String?>(null) } // what busy is doing
    val progressFrac = remember { mutableStateOf<Float?>(null) } // parsed from the tick line: the real bar
    val liveRecv = remember { mutableStateOf<LiveRecv?>(null) } // a direct send's announced row
    val cancelRequested = remember { mutableStateOf(false) } // user canceled the current action
    val ops = remember { kotlinx.coroutines.sync.Mutex() } // serializes all binary execs
    val pendingFiles = remember { mutableStateOf<List<Uri>>(emptyList()) }
    val clipboardSend = remember { mutableStateOf(false) } // pendingFiles came from the clipboard
    val sendTargets = remember { mutableStateOf(setOf<String>()) }
    val invite = remember { mutableStateOf<String?>(null) } // open invite dialog
    val aboutOpen = remember { mutableStateOf<String?>(null) } // open about dialog: the version line
    val resetPending = remember { mutableStateOf(false) } // open reset confirmation dialog
    val snackbar = remember { SnackbarHostState() }

    // A transfer in flight keeps the CPU awake: screen-off suspends
    // the device and stalls the child. Driven from the data path, not
    // the UI — recomposition freezes when the screen is off, so only
    // the child's own output lines can be trusted to fire. Each
    // progress tick re-arms a sliding window; silence lets it drop
    // at the same cap that ends the transfer.
    val wakeLock = remember {
        context.getSystemService(android.os.PowerManager::class.java)
            .newWakeLock(android.os.PowerManager.PARTIAL_WAKE_LOCK, "catbox:transfer")
    }

    // When the last progress line arrived: a live transfer ticks every
    // second, so long silence means the transfer is gone.
    val lastTick = remember { java.util.concurrent.atomic.AtomicLong(0) }

    fun append(line: String) {
        // Progress and completion lines from any child (send, pull,
        // listener) drive the in-UI transfer indicator: the percent
        // feeds the real bar, the text keeps numbers, bandwidth, path.
        val t = line.trim()
        // The announce lines: direct "macbook is sending f directly (378 MiB)",
        // pull "receiving f from macbook (378 MiB)" — both feed the live row.
        val ann = Regex("^(\\S+) is sending (.+) directly \\((.*)\\)$").find(t)
        val pullAnn = Regex("^receiving (.+) from (\\S+) \\((.*)\\)$").find(t)
        when {
            ann != null -> {
                val m = ann.groupValues
                liveRecv.value = LiveRecv(m[1], m[2], m[3], direct = true)
                progress.value = null // ticks take over from here
                progressFrac.value = null
                lastTick.set(System.currentTimeMillis())
                wakeLock.acquire(2 * 60 * 1000L)
            }
            pullAnn != null -> {
                val m = pullAnn.groupValues
                liveRecv.value = LiveRecv(m[2], m[1], m[3], direct = false)
                progress.value = null // the row carries it
                progressFrac.value = null
                lastTick.set(System.currentTimeMillis())
            }
            t.startsWith("sending ") || t.startsWith("received ") || t.startsWith("depositing ") -> {
                progressFrac.value = Regex("\\((\\d+)%\\)").find(t)?.groupValues?.get(1)?.toFloat()?.div(100f)
                if (liveRecv.value == null) {
                    // The path tail always overflows one line: it gets its own.
                    progress.value = Regex("\\s*\\(\\d+%\\)").replace(t, "").replace(", path: ", "\npath: ").replaceFirstChar { it.uppercase() }
                } else {
                    // A live receive shows its own row; the tick only
                    // feeds the bar and the row's path.
                    liveRecv.value = liveRecv.value?.copy(path = Regex(", path: (.+)$").find(t)?.groupValues?.get(1))
                }
                lastTick.set(System.currentTimeMillis())
                wakeLock.acquire(2 * 60 * 1000L)
            }
            t.startsWith("got ") || t.startsWith("sent ") || t.startsWith("dismissed ") ||
                t.startsWith("receive failed") -> {
                progress.value = null
                progressFrac.value = null
                liveRecv.value = null
                if (wakeLock.isHeld) wakeLock.release()
            }
        }
    }

    // A background status poll: skips silently when an action holds
    // the ops mutex, and never disables the buttons.
    suspend fun refresh() {
        if (!ops.tryLock()) return
        try {
            val lines = mutableListOf<String>()
            val s = withContext(Dispatchers.IO) { Catbox.status(context) { lines.add(it) } }
            // Whatever landed (direct or pulled), promote it to the public
            // Download/Catbox folder.
            withContext(Dispatchers.IO) { Catbox.publish(context) }
            status.value = s
            joined.value = s != null || !Catbox.noIdentity(lines)
        } finally {
            ops.unlock()
        }
    }

    suspend fun run(vararg args: String) {
        busy.value = true
        ops.lock()
        // The snackbar comes after the finally: the bar must not outlive it.
        val code = try {
            withContext(Dispatchers.IO) { Catbox.run(context, ::append, *args) }
        } finally {
            ops.unlock()
            busy.value = false
        }

        when {
            code != 0 && cancelRequested.value -> snackbar.showSnackbar("Canceled")
            code != 0 -> snackbar.showSnackbar("Failed (exit $code)")
        }

        cancelRequested.value = false
    }

    suspend fun sendAll(uris: List<Uri>, targets: Set<String>) {
        busy.value = true
        ops.lock()
        val problems = mutableListOf<String>()
        val total = uris.size * targets.size
        var done = 0
        var canceled = false

        try {
            for (target in targets) {
                if (cancelRequested.value) break

                for (uri in uris) {
                    if (cancelRequested.value) break

                    done++
                    val tmp = withContext(Dispatchers.IO) {
                        val f = File(context.cacheDir, displayName(context, uri))
                        context.contentResolver.openInputStream(uri)!!.use { input ->
                            f.outputStream().use { input.copyTo(it) }
                        }
                        f
                    }
                    progress.value = "Sending $done/$total: ${tmp.name}"
                    progressFrac.value = null // staging: no percent yet
                    val code = withContext(Dispatchers.IO) { Catbox.run(context, ::append, "send", target, tmp.absolutePath) }
                    tmp.delete()
                    if (code != 0 && !cancelRequested.value) problems += "Send to $target failed (exit $code)"
                }
            }
        } finally {
            canceled = cancelRequested.value
            ops.unlock()
            busy.value = false
            progress.value = null
            cancelRequested.value = false
        }
        // Snackbars only after busy clears: the bar never outlives the message.
        if (canceled) {
            snackbar.showSnackbar("Canceled")
        } else if (problems.isEmpty()) {
            val n = uris.size
            snackbar.showSnackbar("Sent ${if (n == 1) "1 file" else "$n files"} to ${targets.joinToString()}")
        } else {
            snackbar.showSnackbar(problems.joinToString(" · "))
        }
        refresh()
    }

    val pickFiles = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) {
            pendingFiles.value = uris
            clipboardSend.value = false
            sendTargets.value = emptySet()
        }
    }

    // The clipboard as a sendable: a copied file's content URI goes
    // straight through; plain text becomes clipboard-<stamp>.txt in
    // the cache, served via the FileProvider so staging can read it.
    fun sendClipboard() {
        val cm = context.getSystemService(android.content.ClipboardManager::class.java)
        val clip = cm.primaryClip

        val uri = if (clip == null || clip.itemCount == 0) {
            null
        } else {
            clip.getItemAt(0).uri ?: clip.getItemAt(0).text?.let { text ->
                val stamp = java.time.LocalDateTime.now()
                    .format(java.time.format.DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss"))
                val f = File(File(context.cacheDir, "clipboard"), "clipboard-$stamp.txt")
                f.parentFile!!.mkdirs()
                f.writeText(text.toString())
                androidx.core.content.FileProvider.getUriForFile(context, context.packageName + ".files", f)
            }
        }

        if (uri == null) {
            scope.launch { snackbar.showSnackbar("Clipboard has nothing sendable") }
            return
        }

        pendingFiles.value = listOf(uri)
        clipboardSend.value = true
        sendTargets.value = emptySet()
    }

    LaunchedEffect(Unit) {
        refresh()
        // Keep the indicator honest; offline polls are cheap and
        // frequent so recovery shows up fast.
        while (true) {
            delay(if (status.value == null) 5_000 else 15_000)
            refresh()
        }
    }

    // A stalled indicator clears itself: no tick for half a minute
    // means the child is frozen or dead without a completion line —
    // a vanished sender, a lock that killed the listener mid-receive.
    LaunchedEffect(Unit) {
        while (true) {
            delay(5_000)
            if ((progress.value != null || liveRecv.value != null) && System.currentTimeMillis() - lastTick.get() > 35_000) {
                progress.value = null
                progressFrac.value = null
                liveRecv.value = null
                if (wakeLock.isHeld) wakeLock.release()
            }
        }
    }

    // The listener follows the app's visibility: direct sends are
    // always welcome while catbox is on screen, while storer pulls
    // wait behind each waiting file's receive button.
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(joined.value) {
        if (joined.value == true) Catbox.startListener(context, ::append)
    }
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_START -> if (joined.value == true) Catbox.startListener(context, ::append)
                Lifecycle.Event.ON_STOP -> {
                    Catbox.stopListener()
                    // The listener child died mid-receive: no completion
                    // line is coming — the indicator must not survive it.
                    if (progress.value != null && !busy.value) {
                        progress.value = null
                        progressFrac.value = null
                        if (wakeLock.isHeld) wakeLock.release()
                    }
                    liveRecv.value = null // the direct receive died with the listener
                }
                else -> {}
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    val menuOpen = remember { mutableStateOf(false) }
    val refreshing = remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Image(
                            painter = painterResource(R.drawable.tailcat),
                            contentDescription = null,
                            modifier = Modifier.size(24.dp),
                        )
                        Spacer(Modifier.size(8.dp))
                        Text("Catbox")
                    }
                },
                actions = {
                    if (joined.value == true) {
                        // The status indicator lives in the header: the
                        // dot says everything, the word says the rest.
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(end = 12.dp)) {
                            Text(
                                if (status.value != null) "Online" else "Offline",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                            Spacer(Modifier.size(4.dp))
                            Dot(online = status.value != null)
                        }
                        IconButton(onClick = {
                            scope.launch {
                                refreshing.value = true
                                refresh()
                                refreshing.value = false
                            }
                        }) {
                            if (refreshing.value || status.value == null) {
                                CircularProgressIndicator(Modifier.size(24.dp))
                            } else {
                                Icon(Icons.Outlined.Refresh, "Refresh")
                            }
                        }
                        IconButton(onClick = { menuOpen.value = true }) {
                            Icon(Icons.Outlined.MoreVert, "Menu")
                        }
                        DropdownMenu(expanded = menuOpen.value, onDismissRequest = { menuOpen.value = false }) {
                            DropdownMenuItem(
                                text = { Text("Invite a device") },
                                onClick = {
                                    menuOpen.value = false
                                    scope.launch {
                                        invite.value = withContext(Dispatchers.IO) { Catbox.invite(context) } ?: ""
                                    }
                                },
                            )
                            DropdownMenuItem(
                                text = { Text("About") },
                                onClick = {
                                    menuOpen.value = false
                                    scope.launch {
                                        aboutOpen.value = withContext(Dispatchers.IO) { Catbox.version(context) }
                                    }
                                },
                            )
                            DropdownMenuItem(
                                text = { Text("Reset") },
                                onClick = {
                                    menuOpen.value = false
                                    resetPending.value = true
                                },
                            )
                        }
                    }
                },
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
        bottomBar = {
            if (joined.value == true) {
                Column(
                    Modifier
                        .fillMaxWidth()
                        .navigationBarsPadding()
                        .padding(horizontal = 16.dp, vertical = 8.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    // Own send actions only: receives — pull or direct —
                    // live entirely on their file row in the list.
                    if (busy.value && liveRecv.value == null) {
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                            Text(
                                progress.value ?: "Working…", // queued, hashing, or stalled: never a dark screen
                                style = MaterialTheme.typography.bodySmall,
                                maxLines = 2,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.weight(1f),
                            )
                            if (progress.value != null) {
                                IconButton(onClick = {
                                    scope.launch {
                                        if (busy.value) {
                                            // Cancel an own action (pull/send): kill the
                                            // child; a pulled item was never acked and stays
                                            // parked, its partial kept for the resume.
                                            cancelRequested.value = true
                                            Catbox.cancelAction()
                                            progress.value = null
                                        } else {
                                            // Cancel a direct receive: bounce the
                                            // listener — the transfer dies with the
                                            // old child, the restart waits out its
                                            // deregister so the roster address
                                            // isn't wiped late. The partial stays
                                            // for the resume; the binary sweeps
                                            // stale ones by TTL.
                                            Catbox.stopListener()
                                            progress.value = null
                                            progressFrac.value = null
                                            liveRecv.value = null
                                            if (wakeLock.isHeld) wakeLock.release()
                                            delay(1500)
                                            Catbox.startListener(context, ::append)
                                        }
                                    }
                                }) {
                                    Icon(Icons.Outlined.Close, contentDescription = "Cancel")
                                }
                            }
                        }
                    }
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                    ) {
                        Button(
                            onClick = { pickFiles.launch(arrayOf("*/*")) },
                            enabled = !busy.value,
                        ) {
                            Text("Send")
                        }
                        IconButton(onClick = { sendClipboard() }, enabled = !busy.value) {
                            Icon(Icons.Outlined.ContentPaste, contentDescription = "Send clipboard")
                        }
                    }
                }
            }
        },
    ) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).padding(horizontal = 16.dp)) {
            when (joined.value) {
                null -> Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    Text("Checking…")
                }
                false -> Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    JoinCard { addr, name -> scope.launch { run("join", "--name", name, addr); refresh() } }
                }
                else -> MainScreen(
                    status.value,
                    busy.value,
                    liveRecv.value,
                    progressFrac.value,
                    onCancelRecv = {
                        scope.launch {
                            if (busy.value) {
                                // Cancel a pull: kill the child; its ctx sweep
                                // removes the partial, the item was never acked
                                // and stays parked.
                                cancelRequested.value = true
                                Catbox.cancelAction()
                            } else {
                                // Cancel a direct receive: bounce the listener
                                // and sweep the partials — with the listener
                                // stopped nothing can be writing them, and a
                                // canceled receive must leave nothing behind.
                                Catbox.stopListener()
                                Catbox.sweepPartials(context)
                                delay(1500)
                                Catbox.startListener(context, ::append)
                            }
                            progress.value = null
                            progressFrac.value = null
                            liveRecv.value = null
                            if (wakeLock.isHeld) wakeLock.release()
                        }
                    },
                    onReceive = { id -> scope.launch { run("recv", "--dir", Catbox.inbox(context).absolutePath, id); refresh() } },
                    onReceiveAll = { scope.launch { run("recv", "--dir", Catbox.inbox(context).absolutePath); refresh() } },
                    onDismiss = { id -> scope.launch { run("dismiss", id); refresh() } },
                )
            }
        }

        // Step 2 of send: files picked, pick peers with checkboxes.
        if (pendingFiles.value.isNotEmpty()) {
            val me = Catbox.cachedName(context)
            val peers = Catbox.cachedMembers(context).filter { it != me }.sorted()
            AlertDialog(
                onDismissRequest = {
                    pendingFiles.value = emptyList()
                    sendTargets.value = emptySet()
                },
                title = {
                    val n = pendingFiles.value.size
                    Text(
                        if (clipboardSend.value) {
                            "Send clipboard content to"
                        } else {
                            "Send ${if (n == 1) "file" else "$n files"} to"
                        },
                    )
                },
                text = {
                    Column {
                        if (peers.isEmpty()) {
                            Text("No other members in the roster")
                        }
                        peers.forEach { peer ->
                            ListItem(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .toggleable(
                                        value = peer in sendTargets.value,
                                        role = Role.Checkbox,
                                        onValueChange = {
                                            sendTargets.value =
                                                if (peer in sendTargets.value) sendTargets.value - peer
                                                else sendTargets.value + peer
                                        },
                                    ),
                                leadingContent = { Checkbox(checked = peer in sendTargets.value, onCheckedChange = null) },
                                headlineContent = { Text(peer) },
                            )
                        }
                    }
                },
                confirmButton = {
                    TextButton(
                        enabled = sendTargets.value.isNotEmpty(),
                        onClick = {
                            val files = pendingFiles.value
                            val targets = sendTargets.value
                            pendingFiles.value = emptyList()
                            sendTargets.value = emptySet()
                            scope.launch { sendAll(files, targets) }
                        },
                    ) {
                        Text("Send")
                    }
                },
                dismissButton = {
                    TextButton(
                        onClick = {
                            pendingFiles.value = emptyList()
                            sendTargets.value = emptySet()
                        },
                    ) {
                        Text("Cancel")
                    }
                },
            )
        }

        // The invite line for enrolling another device: selectable,
        // with a share shortcut.
        invite.value?.let { line ->
            AlertDialog(
                onDismissRequest = { invite.value = null },
                title = { Text("Invite a device") },
                text = {
                    if (line.isEmpty()) {
                        Text("No identity yet — join first")
                    } else {
                        SelectionContainer {
                            Text(line, style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                },
                confirmButton = {
                    if (line.isNotEmpty()) {
                        TextButton(onClick = {
                            val send = android.content.Intent(android.content.Intent.ACTION_SEND).apply {
                                type = "text/plain"
                                putExtra(
                                    android.content.Intent.EXTRA_TEXT,
                                    line,
                                )
                            }
                            context.startActivity(android.content.Intent.createChooser(send, null))
                        }) {
                            Text("Share")
                        }
                    }
                },
                dismissButton = {
                    TextButton(onClick = { invite.value = null }) { Text("Close") }
                },
            )
        }

        // About: the version the bundled binary was built with.
        aboutOpen.value?.let { ver ->
            AlertDialog(
                onDismissRequest = { aboutOpen.value = null },
                title = { Text("About") },
                text = { Text(ver) },
                confirmButton = {
                    TextButton(onClick = { aboutOpen.value = null }) { Text("Close") }
                },
            )
        }

        // Reset: leave the mesh and wipe this device's identity. The
        // binary removes this connection's member (dial-key bound:
        // a device can never reset another) and deletes the identity.
        if (resetPending.value) {
            AlertDialog(
                onDismissRequest = { resetPending.value = false },
                title = { Text("Reset this device?") },
                text = {
                    Text(
                        "It leaves the roster when the storer is reachable, and its identity is wiped either way. It can join again afterwards.",
                    )
                },
                confirmButton = {
                    TextButton(
                        onClick = {
                            resetPending.value = false
                            Catbox.stopListener()
                            scope.launch { run("reset"); refresh() }
                        },
                    ) {
                        Text("Reset")
                    }
                },
                dismissButton = {
                    TextButton(onClick = { resetPending.value = false }) { Text("Cancel") }
                },
            )
        }
    }
}

@Composable
fun Dot(online: Boolean) {
    // Semantic, not a theme role: dynamic tertiary follows the
    // wallpaper and can land on red.
    val green = if (isSystemInDarkTheme()) Color(0xFF81C784) else Color(0xFF2E7D32)
    Box(
        Modifier
            .size(10.dp)
            .background(
                color = if (online) green else MaterialTheme.colorScheme.error,
                shape = CircleShape,
            ),
    )
}

@Composable
fun MainScreen(
    status: Catbox.Status?,
    busy: Boolean,
    liveRecv: LiveRecv?,
    liveFrac: Float?,
    onCancelRecv: () -> Unit,
    onReceive: (String) -> Unit,
    onReceiveAll: () -> Unit,
    onDismiss: (String) -> Unit,
) {
    val waiting = status?.waiting.orEmpty()
    val context = LocalContext.current
    var published by remember { mutableStateOf(emptyList<Catbox.Published>()) }
    var dismissPending by remember { mutableStateOf<Catbox.Status.Wait?>(null) }

    // MediaStore is disk I/O: query off the main thread, never in
    // composition. Direct receives change neither status nor busy, so
    // a 5s tick surfaces them without a manual tap.
    suspend fun syncPublished() {
        published = withContext(Dispatchers.IO) { Catbox.publish(context); Catbox.published(context) }
    }

    // A direct receive flips neither status nor busy: the live row's
    // disappearance is the completion signal that publishes at once,
    // instead of waiting out the 5s poll.
    LaunchedEffect(status, busy, liveRecv) { syncPublished() }

    LaunchedEffect(Unit) {
        while (true) {
            delay(5_000)
            syncPublished()
        }
    }

    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (waiting.isEmpty() && published.isEmpty() && liveRecv == null) {
            // Nothing to scroll: no LazyColumn, so no nudgeable empty state.
            Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                Text(
                    "Nothing received yet",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        } else {
            // The files: one scroll area, sections by state.
            LazyColumn(Modifier.weight(1f).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                // A pull transforms its own waiting row in place, matched
                // by name AND sender — same-name files from different
                // senders must not steal each other's bar. A direct
                // receive never parks — it loads in the received zone.
                val liveIsWaiting = liveRecv != null && !liveRecv.direct &&
                    waiting.any { it.fn == liveRecv.file && it.from == liveRecv.from }
                if (waiting.isNotEmpty()) {
                    item {
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                            SectionLabel("Waiting", Modifier.weight(1f))
                            IconButton(onClick = onReceiveAll) {
                                Icon(Icons.Outlined.Download, contentDescription = "Receive all")
                            }
                        }
                    }
                    items(waiting, key = { it.id }) { f ->
                        if (liveIsWaiting && f.fn == liveRecv.file && f.from == liveRecv.from) {
                            // In flight: the download/dismiss row becomes
                            // the progress/cancel row, in place.
                            LiveItem(f.fn, f.from, liveRecv.detail, liveRecv.path, liveFrac, onCancelRecv, Modifier.animateItem())
                        } else {
                            ListItem(
                                headlineContent = {
                                    Text(
                                        f.fn,
                                        maxLines = 1, // a long name ellipsizes, never wraps
                                        overflow = TextOverflow.Ellipsis,
                                    )
                                },
                                supportingContent = {
                                    Column {
                                        Text("From ${f.from}")
                                        Text(humanBytes(f.plain))
                                    }
                                },
                                trailingContent = {
                                    Row {
                                        IconButton(onClick = { onReceive(f.id) }) {
                                            Icon(Icons.Outlined.Download, contentDescription = "Receive")
                                        }
                                        IconButton(onClick = { dismissPending = f }) {
                                            Icon(Icons.Outlined.Delete, contentDescription = "Dismiss")
                                        }
                                    }
                                },
                                modifier = Modifier.animateItem(),
                            )
                        }
                    }
                }
                item {
                    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                        SectionLabel("Received", Modifier.weight(1f))
                        IconButton(onClick = { openInFilesApp(context) }) {
                            Icon(Icons.AutoMirrored.Outlined.ExitToApp, "Open in Files")
                        }
                    }
                }
                // A direct receive loads right here: the file appears in
                // the received zone, and on completion the published entry
                // takes the row's place.
                if (liveRecv != null && !liveIsWaiting) {
                    item(key = "live-recv") {
                        LiveItem(liveRecv.file, liveRecv.from, liveRecv.detail, liveRecv.path, liveFrac, onCancelRecv, Modifier.animateItem())
                    }
                }
                items(published, key = { it.uri.toString() }) { f ->
                    ListItem(
                        headlineContent = { Text(f.name) },
                        supportingContent = { Text("${humanBytes(f.size)} · ${humanWhen(f.date)}") },
                        trailingContent = {
                            IconButton(onClick = { sharePublished(context, f) }) {
                                Icon(Icons.Outlined.Share, "Share")
                            }
                        },
                        modifier = Modifier.animateItem().clickable { openPublished(context, f) },
                    )
                }
            }
        }
    }

    // Refusing delivery: full info, then the storer drops it unread.
    dismissPending?.let { f ->
        AlertDialog(
            onDismissRequest = { dismissPending = null },
            title = { Text("Dismiss file") },
            text = {
                Text(
                    "${f.fn} from ${f.from} (${humanBytes(f.plain)}) will be deleted at the storer without being received.",
                )
            },
            confirmButton = {
                TextButton(onClick = { onDismiss(f.id); dismissPending = null }) { Text("Dismiss") }
            },
            dismissButton = {
                TextButton(onClick = { dismissPending = null }) { Text("Cancel") }
            },
        )
    }
}

@Composable
fun SectionLabel(text: String, modifier: Modifier) {
    Text(
        text.uppercase(),
        style = MaterialTheme.typography.labelSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = modifier.padding(top = 8.dp),
    )
}

/** One file being received — direct or pull: same row shape. */
@Composable
fun LiveItem(file: String, from: String, detail: String, path: String?, frac: Float?, onCancel: () -> Unit, modifier: Modifier = Modifier) {
    ListItem(
        headlineContent = {
            Text(
                file,
                maxLines = 1, // a long name ellipsizes, never wraps
                overflow = TextOverflow.Ellipsis,
            )
        },
        supportingContent = {
            Column {
                Text("From $from")
                Text(
                    if (path == null) detail else "$detail · $path",
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                frac?.let { f ->
                    LinearProgressIndicator(progress = { f }, modifier = Modifier.fillMaxWidth())
                } ?: LinearProgressIndicator(Modifier.fillMaxWidth())
            }
        },
        trailingContent = {
            IconButton(onClick = onCancel) {
                Icon(Icons.Outlined.Close, contentDescription = "Cancel")
            }
        },
        modifier = modifier, // callers inside the list animate the item
    )
}

/** The paste is often the whole invite line; keep only its address. */
fun storerAddressOf(paste: String): String {
    val t = paste.trim()
    return if (t.startsWith("catbox join ")) t.split(Regex("\\s+")).last() else t
}

fun humanBytes(n: Long): String = when {
    n < 1024 -> "$n B"
    n < 1024 * 1024 -> "%.1f KiB".format(n / 1024.0)
    n < 1024L * 1024 * 1024 -> "%.1f MiB".format(n / 1024.0 / 1024.0)
    else -> "%.1f GiB".format(n / 1024.0 / 1024.0 / 1024.0)
}

/** "d MMM yyyy HH:mm", in the local zone. */
fun humanWhen(epoch: Long): String {
    val t = java.time.Instant.ofEpochSecond(epoch).atZone(java.time.ZoneId.systemDefault())
    return t.format(java.time.format.DateTimeFormatter.ofPattern("d MMM yyyy HH:mm"))
}

/**
 * The real display name of a SAF document: its URI path is a provider
 * row ID ("1000059202"), not a name, so ask the provider. A digits-only
 * stem is a provider row id in disguise, never a name a human chose:
 * send it as clipboard-<stamp> instead.
 */
fun displayName(context: android.content.Context, uri: Uri): String {
    var name = ""
    context.contentResolver.query(
        uri,
        arrayOf(android.provider.OpenableColumns.DISPLAY_NAME),
        null,
        null,
        null,
    )?.use { c ->
        if (c.moveToFirst()) name = c.getString(0) ?: ""
    }

    if (name.isEmpty()) name = uri.lastPathSegment?.substringAfterLast('/') ?: ""

    val stem = name.substringBeforeLast('.')
    val ext = name.substringAfterLast('.', "")

    if (stem.isEmpty() || stem.all { it.isDigit() }) {
        val stamp = java.time.LocalDateTime.now()
            .format(java.time.format.DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss"))
        return "clipboard-$stamp" + if (ext.isNotEmpty()) ".$ext" else ""
    }

    return name
}

/**
 * Opens the public Download/Catbox folder in the system Files app.
 * The directory mime type is what makes documentsui navigate instead
 * of offering to open the file with something.
 */
fun openInFilesApp(context: android.content.Context) {
    val uri = android.provider.DocumentsContract.buildDocumentUri(
        "com.android.externalstorage.documents",
        "primary:Download/Catbox",
    )
    val intent = android.content.Intent(android.content.Intent.ACTION_VIEW).apply {
        setDataAndType(uri, "vnd.android.document/directory")
        addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
    }
    context.startActivity(intent)
}

/**
 * Opens a published file; the real MIME type decides which apps offer
 * to handle it (octet-stream matches nothing).
 */
fun openPublished(context: android.content.Context, f: Catbox.Published) {
    val intent = android.content.Intent(android.content.Intent.ACTION_VIEW).apply {
        setDataAndType(f.uri, Catbox.mimeOf(f.name))
        addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    context.startActivity(intent)
}

/**
 * Shares a published file through the system sheet.
 */
fun sharePublished(context: android.content.Context, f: Catbox.Published) {
    val send = android.content.Intent(android.content.Intent.ACTION_SEND).apply {
        type = Catbox.mimeOf(f.name)
        putExtra(android.content.Intent.EXTRA_STREAM, f.uri)
        addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    context.startActivity(android.content.Intent.createChooser(send, null))
}

@Composable
fun JoinCard(onJoin: (String, String) -> Unit) {
    var addr by remember { mutableStateOf("") }
    var name by remember { mutableStateOf("") }
    ElevatedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Join a catbox", style = MaterialTheme.typography.titleMedium)
            Text(
                "Paste the storer address from `catbox invite` on one of your machines, and pick this device's name.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            OutlinedTextField(
                value = addr,
                onValueChange = { addr = it },
                label = { Text("Storer address (tc…)") },
                singleLine = true,
            )
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("This device's name") },
                singleLine = true,
            )
            Button(
                onClick = { onJoin(storerAddressOf(addr), name.trim()) },
                enabled = addr.isNotEmpty() && name.isNotEmpty(),
            ) {
                Text("Join")
            }
        }
    }
}
