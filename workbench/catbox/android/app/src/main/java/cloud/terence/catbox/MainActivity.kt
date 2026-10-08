package cloud.terence.catbox

import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.material3.OutlinedButton
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

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CatboxApp() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val status = remember { mutableStateOf<Catbox.Status?>(null) }
    val joined = remember { mutableStateOf<Boolean?>(null) } // null = checking
    val busy = remember { mutableStateOf(false) } // an action (receive/send) is running
    val progress = remember { mutableStateOf<String?>(null) } // what busy is doing
    val cancelRequested = remember { mutableStateOf(false) } // user canceled the current action
    val ops = remember { kotlinx.coroutines.sync.Mutex() } // serializes all binary execs
    val pendingFiles = remember { mutableStateOf<List<Uri>>(emptyList()) }
    val clipboardSend = remember { mutableStateOf(false) } // pendingFiles came from the clipboard
    val sendTargets = remember { mutableStateOf(setOf<String>()) }
    val invite = remember { mutableStateOf<String?>(null) } // open invite dialog
    val aboutOpen = remember { mutableStateOf<String?>(null) } // open about dialog: the version line
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

    fun append(line: String) {
        // Progress and completion lines from any child (send, pull,
        // listener) drive the in-UI transfer indicator.
        val t = line.trim()
        when {
            t.startsWith("sending ") || t.startsWith("received ") || t.startsWith("depositing ") -> {
                progress.value = t
                wakeLock.acquire(2 * 60 * 1000L)
            }
            t.startsWith("got ") || t.startsWith("sent ") || t.startsWith("dismissed ") ||
                t.startsWith("receive failed") -> {
                progress.value = null
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
            code != 0 && cancelRequested.value -> snackbar.showSnackbar("canceled")
            code != 0 -> snackbar.showSnackbar("failed (exit $code)")
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
                    progress.value = "sending $done/$total: ${tmp.name}"
                    val code = withContext(Dispatchers.IO) { Catbox.run(context, ::append, "send", target, tmp.absolutePath) }
                    tmp.delete()
                    if (code != 0 && !cancelRequested.value) problems += "send to $target failed (exit $code)"
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
            snackbar.showSnackbar("canceled")
        } else if (problems.isEmpty()) {
            val n = uris.size
            snackbar.showSnackbar("sent ${if (n == 1) "1 file" else "$n files"} to ${targets.joinToString()}")
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
            scope.launch { snackbar.showSnackbar("clipboard has nothing sendable") }
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

    // The listener follows the app's visibility: direct sends are
    // always welcome while catbox is on screen, while storer pulls
    // stay behind the explicit receive button.
    val lifecycleOwner = LocalLifecycleOwner.current
    LaunchedEffect(joined.value) {
        if (joined.value == true) Catbox.startListener(context, ::append)
    }
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_START -> if (joined.value == true) Catbox.startListener(context, ::append)
                Lifecycle.Event.ON_STOP -> Catbox.stopListener()
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
                title = { Text("catbox") },
                actions = {
                    if (joined.value == true) {
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
                                Icon(Icons.Outlined.Refresh, "refresh")
                            }
                        }
                        IconButton(onClick = { menuOpen.value = true }) {
                            Icon(Icons.Outlined.MoreVert, "menu")
                        }
                        DropdownMenu(expanded = menuOpen.value, onDismissRequest = { menuOpen.value = false }) {
                            DropdownMenuItem(
                                text = { Text("invite a device") },
                                onClick = {
                                    menuOpen.value = false
                                    scope.launch {
                                        invite.value = withContext(Dispatchers.IO) { Catbox.invite(context) } ?: ""
                                    }
                                },
                            )
                            DropdownMenuItem(
                                text = { Text("about") },
                                onClick = {
                                    menuOpen.value = false
                                    scope.launch {
                                        aboutOpen.value = withContext(Dispatchers.IO) { Catbox.version(context) }
                                    }
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
                    // The indicator covers own actions and live
                    // transfers (direct receives never flip busy).
                    if (busy.value || progress.value != null) {
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                            progress.value?.let {
                                Text(
                                    it,
                                    style = MaterialTheme.typography.bodySmall,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                    modifier = Modifier.weight(1f),
                                )
                            }
                            if (progress.value != null) {
                                TextButton(
                                    onClick = {
                                        scope.launch {
                                            if (busy.value) {
                                                // Cancel an own action (pull/send):
                                                // kill the child; a pulled item was
                                                // never acked and stays parked, its
                                                // partial kept for the resume.
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
                                                delay(1500)
                                                Catbox.startListener(context, ::append)
                                            }
                                        }
                                    },
                                ) {
                                    Text("cancel")
                                }
                            }
                        }
                        LinearProgressIndicator(Modifier.fillMaxWidth())
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        Button(
                            onClick = {
                                scope.launch {
                                    run("recv", "--dir", Catbox.inbox(context).absolutePath)
                                    refresh()
                                }
                            },
                            enabled = !busy.value,
                            modifier = Modifier.weight(1f),
                        ) {
                            Text("receive")
                        }
                        OutlinedButton(
                            onClick = { pickFiles.launch(arrayOf("*/*")) },
                            enabled = !busy.value,
                            modifier = Modifier.weight(1f),
                        ) {
                            Text("send")
                        }
                        IconButton(onClick = { sendClipboard() }, enabled = !busy.value) {
                            Icon(
                                painterResource(R.drawable.ic_clipboard),
                                contentDescription = "send clipboard",
                            )
                        }
                    }
                }
            }
        },
    ) { padding ->
        Column(Modifier.fillMaxSize().padding(padding).padding(horizontal = 16.dp)) {
            when (joined.value) {
                null -> Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    Text("checking…")
                }
                false -> Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                    JoinCard { addr, name -> scope.launch { run("join", "--name", name, addr); refresh() } }
                }
                else -> MainScreen(
                    status.value,
                    busy.value,
                    onDismiss = { id -> scope.launch { run("dismiss", id); refresh() } },
                )
            }
        }

        // Step 2 of send: files picked, pick peers with checkboxes.
        if (pendingFiles.value.isNotEmpty()) {
            val me = Catbox.cachedName(context)
            val peers = Catbox.cachedMembers(context).filter { it != me }
            AlertDialog(
                onDismissRequest = {
                    pendingFiles.value = emptyList()
                    sendTargets.value = emptySet()
                },
                title = {
                    val n = pendingFiles.value.size
                    Text(
                        if (clipboardSend.value) {
                            "send clipboard content to"
                        } else {
                            "send ${if (n == 1) "file" else "$n files"} to"
                        },
                    )
                },
                text = {
                    Column {
                        if (peers.isEmpty()) {
                            Text("no other members in the roster")
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
                        Text("send")
                    }
                },
                dismissButton = {
                    TextButton(
                        onClick = {
                            pendingFiles.value = emptyList()
                            sendTargets.value = emptySet()
                        },
                    ) {
                        Text("cancel")
                    }
                },
            )
        }

        // The invite line for enrolling another device: selectable,
        // with a share shortcut.
        invite.value?.let { line ->
            AlertDialog(
                onDismissRequest = { invite.value = null },
                title = { Text("invite a device") },
                text = {
                    if (line.isEmpty()) {
                        Text("no identity yet — join first")
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
                            Text("share")
                        }
                    }
                },
                dismissButton = {
                    TextButton(onClick = { invite.value = null }) { Text("close") }
                },
            )
        }

        // About: the version the bundled binary was built with.
        aboutOpen.value?.let { ver ->
            AlertDialog(
                onDismissRequest = { aboutOpen.value = null },
                title = { Text("about") },
                text = { Text(ver) },
                confirmButton = {
                    TextButton(onClick = { aboutOpen.value = null }) { Text("close") }
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
fun MainScreen(status: Catbox.Status?, busy: Boolean, onDismiss: (String) -> Unit) {
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

    LaunchedEffect(status, busy) { syncPublished() }

    LaunchedEffect(Unit) {
        while (true) {
            delay(5_000)
            syncPublished()
        }
    }

    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        // Quiet status row: the dot says everything.
        Row(verticalAlignment = Alignment.CenterVertically) {
            Dot(online = status != null)
            Spacer(Modifier.size(8.dp))
            Text(
                if (status != null) "connected" else "offline",
                style = MaterialTheme.typography.titleSmall,
            )
        }

        if (waiting.isEmpty() && published.isEmpty()) {
            // Nothing to scroll: no LazyColumn, so no nudgeable empty state.
            Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                Text(
                    "nothing received yet",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        } else {
            // The files: one scroll area, sections by state.
            LazyColumn(Modifier.weight(1f).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                if (waiting.isNotEmpty()) {
                    item { SectionLabel("waiting", Modifier) }
                    items(waiting, key = { it.id }) { f ->
                        ListItem(
                            headlineContent = { Text(f.fn) },
                            supportingContent = { Text("from ${f.from} · ${humanBytes(f.plain)}") },
                            trailingContent = {
                                TextButton(onClick = { dismissPending = f }) { Text("dismiss") }
                            },
                            modifier = Modifier.animateItem(),
                        )
                    }
                }
                item {
                    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                        SectionLabel("received", Modifier.weight(1f))
                        IconButton(onClick = { openInFilesApp(context) }) {
                            Icon(Icons.AutoMirrored.Outlined.ExitToApp, "open in Files")
                        }
                    }
                }
                items(published, key = { it.uri.toString() }) { f ->
                    ListItem(
                        headlineContent = { Text(f.name) },
                        supportingContent = { Text("${humanBytes(f.size)} · ${humanWhen(f.date)}") },
                        trailingContent = {
                            IconButton(onClick = { sharePublished(context, f) }) {
                                Icon(Icons.Outlined.Share, "share")
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
            title = { Text("dismiss file") },
            text = {
                Text(
                    "${f.fn} from ${f.from} (${humanBytes(f.plain)}) will be deleted at the storer without being received.",
                )
            },
            confirmButton = {
                TextButton(onClick = { onDismiss(f.id); dismissPending = null }) { Text("dismiss") }
            },
            dismissButton = {
                TextButton(onClick = { dismissPending = null }) { Text("cancel") }
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
                label = { Text("storer address (tc…)") },
                singleLine = true,
            )
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("this device's name") },
                singleLine = true,
            )
            Button(
                onClick = { onJoin(addr.trim(), name.trim()) },
                enabled = addr.isNotEmpty() && name.isNotEmpty(),
            ) {
                Text("join")
            }
        }
    }
}
