package cloud.terence.catbox

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

/**
 * Material 3 with dynamic color (Material You) on Android 12+, and a
 * hand-tuned warm "ginger cat" scheme as the fallback for older
 * devices, dark variants for both, following the system theme.
 */

private val LightColors = lightColorScheme(
    primary = Color(0xFF8F4C00),
    onPrimary = Color(0xFFFFFFFF),
    primaryContainer = Color(0xFFFFDCC2),
    onPrimaryContainer = Color(0xFF2E1500),
    secondary = Color(0xFF745B44),
    onSecondary = Color(0xFFFFFFFF),
    secondaryContainer = Color(0xFFF9E0C9),
    onSecondaryContainer = Color(0xFF2A1809),
    tertiary = Color(0xFF5D6236),
    onTertiary = Color(0xFFFFFFFF),
    tertiaryContainer = Color(0xFFE1E4B6),
    onTertiaryContainer = Color(0xFF1A1E00),
)

private val DarkColors = darkColorScheme(
    primary = Color(0xFFFFB77D),
    onPrimary = Color(0xFF4E2800),
    primaryContainer = Color(0xFF6B3C00),
    onPrimaryContainer = Color(0xFFFFDCC2),
    secondary = Color(0xFFE4C0A4),
    onSecondary = Color(0xFF402D19),
    secondaryContainer = Color(0xFF5A432D),
    onSecondaryContainer = Color(0xFFF9E0C9),
    tertiary = Color(0xFFC5CA9C),
    onTertiary = Color(0xFF2E3200),
    tertiaryContainer = Color(0xFF454A21),
    onTertiaryContainer = Color(0xFFE1E4B6),
)

@Composable
fun CatboxTheme(
    darkTheme: Boolean = isSystemInDarkTheme(),
    dynamicColor: Boolean = true,
    content: @Composable () -> Unit,
) {
    val colorScheme = when {
        dynamicColor && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S -> {
            val context = LocalContext.current
            if (darkTheme) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        }
        darkTheme -> DarkColors
        else -> LightColors
    }
    MaterialTheme(colorScheme = colorScheme, content = content)
}
