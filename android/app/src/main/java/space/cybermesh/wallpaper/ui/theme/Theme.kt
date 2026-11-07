package space.cybermesh.wallpaper.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

// 与官网/管理面板同源的深色系: 蓝黑底 + 天蓝主色 + 翠绿点缀
private val AWColors = darkColorScheme(
    primary = Color(0xFF7DD3FC),
    onPrimary = Color(0xFF0F172A),
    secondary = Color(0xFF4ADE80),
    onSecondary = Color(0xFF0F172A),
    background = Color(0xFF0F172A),
    onBackground = Color(0xFFE2E8F0),
    surface = Color(0xFF1E293B),
    onSurface = Color(0xFFE2E8F0),
    surfaceVariant = Color(0xFF243447),
    onSurfaceVariant = Color(0xFF94A3B8),
    outline = Color(0xFF334155),
    error = Color(0xFFF87171),
)

@Composable
fun AWTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = AWColors, typography = Typography(), content = content)
}
