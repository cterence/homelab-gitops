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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.ExitToApp
import androidx.compose.material.icons.outlined.List
import androidx.compose.material.icons.outlined.MoreVert
import androidx.compose.material.icons.outlined.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ElevatedCard
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
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
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

@Composable
fun CatboxApp() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val log = remember { MutableStateFlow<List<String>>(emptyList()) }
    val logLines by log.collectAsState()
    val status = remember { mutableStateOf<Catbox.Status?>(null) }
    val errorLine = remember { mutableStateOf<String?>(null) }
    val joined = remember { mutableStateOf<Boolean?>(null) } // null = checking
    val busy = remember { mutableStateOf(false) } // an action (receive/send) is running
    val ops = remember { kotlinx.coroutines.sync.Mutex() } // serializes all binary execs
    val listening = remember { mutableStateOf(false) }
    val logOpen = remember { mutableStateOf(false) }
    val pendingFiles = remember { mutableStateOf<List<Uri>>(emptyList()) }
    val sendTargets = remember { mutableStateOf(setOf<String>()) }
    val invite = remember { mutableStateOf<String?>(null) } // open invite dialog
    val snackbar = remember { SnackbarHostState() }

    fun append(line: String) {
        // slog noise and the status JSON never belong in the pane.
        if (line.startsWith("time=") || line.trim().startsWith("{")) return
        log.value = (log.value + line).takeLast(200)
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
            errorLine.value = lines.lastOrNull { !it.startsWith("time=") && !it.trim().startsWith("{") }
            joined.value = s != null || !Catbox.noIdentity(lines)
        } finally {
            ops.unlock()
        }
    }

    suspend fun run(vararg args: String) {
        busy.value = true
        ops.lock()
        try {
            val code = withContext(Dispatchers.IO) { Catbox.run(context, ::append, *args) }
            if (code != 0) snackbar.showSnackbar("failed (exit $code) — open the log for details")
        } finally {
            ops.unlock()
            busy.value = false
        }
    }

    suspend fun sendAll(uris: List<Uri>, targets: Set<String>) {
        busy.value = true
        ops.lock()
        var failures = 0
        try {
            for (target in targets) {
                for (uri in uris) {
                    val tmp = withContext(Dispatchers.IO) {
                        val f = File(context.cacheDir, displayName(context, uri))
                        context.contentResolver.openInputStream(uri)!!.use { input ->
                            f.outputStream().use { input.copyTo(it) }
                        }
                        f
                    }
                    val code = withContext(Dispatchers.IO) { Catbox.run(context, ::append, "send", target, tmp.absolutePath) }
                    tmp.delete()
                    if (code != 0) {
                        failures++
                        snackbar.showSnackbar("send to $target failed (exit $code) — see log")
                    }
                }
            }
            if (failures == 0) {
                val n = uris.size
                snackbar.showSnackbar("sent ${if (n == 1) "1 file" else "$n files"} to ${targets.joinToString()}")
            }
        } finally {
            ops.unlock()
            busy.value = false
        }
        refresh()
    }

    val pickFiles = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) {
            pendingFiles.value = uris
            sendTargets.value = emptySet()
        }
    }

    LaunchedEffect(Unit) {
        refresh()
        // Keep the indicator honest: a launch-time failure would
        // otherwise paint the card red until the next action.
        while (true) {
            delay(15_000)
            refresh()
        }
    }

    // The listener runs only while the app is on screen: leaving it
    // running in the background means Android eventually kills the
    // frozen child — and its roster address goes stale until then.
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) {
                Catbox.stopListener()
                listening.value = false
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
    }

    Scaffold(
        snackbarHost = { SnackbarHost(snackbar) },
    ) { _ ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .statusBarsPadding()
                .navigationBarsPadding()
                .padding(horizontal = 16.dp, vertical = 8.dp),
        ) {
            when (joined.value) {
                null -> Box(Modifier.weight(1f), contentAlignment = Alignment.Center) {
                    Text("checking…")
                }
                false -> JoinCard { addr, name -> scope.launch { run("join", "--name", name, addr); refresh() } }
                else -> MainScreen(
                    status = status.value,
                    errorLine = errorLine.value,
                    busy = busy.value,
                    listening = listening.value,
                    onRecv = {
                        scope.launch {
                            run("recv", "--dir", Catbox.inbox(context).absolutePath)
                            refresh()
                        }
                    },
                    onSend = { pickFiles.launch(arrayOf("*/*")) },
                    onListen = { on: Boolean ->
                        if (on) {
                            Catbox.startListener(context, ::append)
                            listening.value = true
                        } else {
                            Catbox.stopListener()
                            listening.value = false
                        }
                    },
                    onToggleLog = { logOpen.value = true },
                    onInvite = {
                        scope.launch {
                            invite.value = withContext(Dispatchers.IO) { Catbox.invite(context) } ?: ""
                        }
                    },
                    onRefresh = { scope.launch { refresh() } },
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
                    Text("send ${if (n == 1) "file" else "$n files"} to")
                },
                text = {
                    Column {
                        if (peers.isEmpty()) {
                            Text("no other members in the roster")
                        }
                        peers.forEach { peer ->
                            Row(
                                verticalAlignment = Alignment.CenterVertically,
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clickable {
                                        sendTargets.value =
                                            if (peer in sendTargets.value) sendTargets.value - peer
                                            else sendTargets.value + peer
                                    },
                            ) {
                                Checkbox(
                                    checked = peer in sendTargets.value,
                                    onCheckedChange = {
                                        sendTargets.value =
                                            if (peer in sendTargets.value) sendTargets.value - peer
                                            else sendTargets.value + peer
                                    },
                                )
                                Text(peer)
                            }
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

        // The log: a popup with selectable, scrollable text — the raw
        // output is a debugging aid, not screen furniture.
        if (logOpen.value) {
            AlertDialog(
                onDismissRequest = { logOpen.value = false },
                title = { Text("log") },
                text = {
                    SelectionContainer {
                        Column(
                            Modifier
                                .fillMaxWidth()
                                .heightIn(max = 420.dp)
                                .verticalScroll(rememberScrollState()),
                        ) {
                            logLines.reversed().forEach {
                                Text(it, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                            }
                        }
                    }
                },
                confirmButton = {
                    TextButton(onClick = { logOpen.value = false }) { Text("close") }
                },
            )
        }
    }
}

@Composable
fun Dot(online: Boolean) {
    Box(
        Modifier
            .size(10.dp)
            .background(
                color = if (online) Color(0xFF2E7D32) else Color(0xFFB3261E),
                shape = CircleShape,
            ),
    )
}

@Composable
fun MainScreen(
    status: Catbox.Status?,
    errorLine: String?,
    busy: Boolean,
    listening: Boolean,
    onRecv: () -> Unit,
    onSend: () -> Unit,
    onListen: (Boolean) -> Unit,
    onToggleLog: () -> Unit,
    onInvite: () -> Unit,
    onRefresh: () -> Unit,
) {
    val waiting = status?.waiting.orEmpty()
    val waitBytes = waiting.sumOf { it.plain }
    val context = LocalContext.current
    val published = remember(status, busy) { Catbox.published(context) }

    Column(Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        // Quiet status row: the dot says everything; log and refresh
        // are icons, not words.
        Row(verticalAlignment = Alignment.CenterVertically) {
            Dot(online = status != null)
            Spacer(Modifier.size(8.dp))
            Text(
                if (status != null) "connected" else "offline",
                style = MaterialTheme.typography.titleSmall,
            )
            Spacer(Modifier.weight(1f))
            IconButton(onClick = onInvite) {
                Icon(Icons.Outlined.MoreVert, "invite a device")
            }
            IconButton(onClick = onToggleLog) {
                Icon(Icons.Outlined.List, "log")
            }
            IconButton(onClick = onRefresh) {
                Icon(Icons.Outlined.Refresh, "refresh")
            }
        }

        if (status == null) {
            // First line only: the full story lives in the log popup.
            errorLine?.lineSequence()?.firstOrNull()?.let {
                Text(
                    it,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        }

        // The files: one scroll area, sections by state.
        LazyColumn(Modifier.weight(1f).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            if (waiting.isNotEmpty()) {
                item { SectionLabel("waiting", Modifier) }
                items(waiting, key = { it.fn }) { f ->
                    ListItem(
                        headlineContent = { Text(f.fn) },
                        supportingContent = { Text("from ${f.from} · ${humanBytes(f.plain)}") },
                    )
                }
            }
            item {
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                    SectionLabel("received", Modifier.weight(1f))
                    IconButton(onClick = { openInFilesApp(context) }) {
                        Icon(Icons.Outlined.ExitToApp, "open in Files")
                    }
                }
            }
            items(published, key = { it.name }) { f ->
                ListItem(
                    headlineContent = { Text(f.name) },
                    supportingContent = { Text(humanBytes(f.size)) },
                    trailingContent = {
                        TextButton(onClick = { openPublished(context, f) }) {
                            Text("open")
                        }
                    },
                )
            }
            if (waiting.isEmpty() && published.isEmpty()) {
                item {
                    Text(
                        "no files",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }

        if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())

        // Footer, in thumb reach: the listener as its own quiet row,
        // then the two primary actions.
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.fillMaxWidth().clickable { onListen(!listening) },
        ) {
            Text("receive directly while open", Modifier.weight(1f))
            Switch(checked = listening, onCheckedChange = onListen)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(onClick = onRecv, enabled = !busy, modifier = Modifier.weight(1f)) {
                Text(
                    if (waiting.isEmpty()) "receive" else "receive · ${waiting.size} (${humanBytes(waitBytes)})",
                )
            }
            OutlinedButton(onClick = onSend, enabled = !busy, modifier = Modifier.weight(1f)) {
                Text("send")
            }
        }
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
    n < 1024 * 1024 -> "${n / 1024} KiB"
    n < 1024L * 1024 * 1024 -> "${n / 1024 / 1024} MiB"
    else -> "${n / 1024 / 1024 / 1024} GiB"
}

/**
 * The real display name of a SAF document: its URI path is a provider
 * row ID ("1000059202"), not a name, so ask the provider.
 */
fun displayName(context: android.content.Context, uri: Uri): String {
    context.contentResolver.query(
        uri,
        arrayOf(android.provider.OpenableColumns.DISPLAY_NAME),
        null,
        null,
        null,
    )?.use { c ->
        if (c.moveToFirst()) {
            val n = c.getString(0)
            if (!n.isNullOrEmpty()) return n
        }
    }
    return uri.lastPathSegment?.substringAfterLast('/') ?: "file"
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
 * Opens a published file; .apk files go to the package installer.
 */
fun openPublished(context: android.content.Context, f: Catbox.Published) {
    val intent = android.content.Intent(android.content.Intent.ACTION_VIEW).apply {
        val apk = f.name.endsWith(".apk")
        setDataAndType(f.uri, if (apk) "application/vnd.android.package-archive" else "application/octet-stream")
        addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
    context.startActivity(intent)
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
