import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
}

// 发布签名: 路径与密码放 local.properties(已 gitignore, 严禁入库); 缺省时回退 debug 签名。
val awProps = Properties().apply {
    val f = rootProject.file("local.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "space.cybermesh.wallpaper"
    compileSdk = 34

    defaultConfig {
        applicationId = "space.cybermesh.wallpaper"
        minSdk = 26
        targetSdk = 34
        versionCode = 17
        versionName = "1.1.11"
    }

    signingConfigs {
        create("release") {
            val ks = awProps.getProperty("aw.keystore")
            if (ks != null) {
                storeFile = rootProject.file(ks)
                storePassword = awProps.getProperty("aw.keystore.pass")
                keyAlias = awProps.getProperty("aw.key.alias", "aiwallpaper")
                keyPassword = awProps.getProperty("aw.keystore.pass")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = if (awProps.getProperty("aw.keystore") != null) {
                signingConfigs.getByName("release")
            } else {
                signingConfigs.getByName("debug")
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        compose = true
    }
    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }
    lint {
        // 低内存机器上 lintVital 会造成原生内存耗尽(JVM crash); 发布构建跳过静态扫描
        checkReleaseBuilds = false
        abortOnError = false
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.06.00")
    implementation(composeBom)
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity-compose:1.9.0")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.2")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.2")
    implementation("androidx.navigation:navigation-compose:2.7.7")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.6.3")
    implementation("androidx.datastore:datastore-preferences:1.1.1")
    implementation("io.coil-kt:coil-compose:2.6.0")
    implementation("androidx.work:work-runtime-ktx:2.9.0")
}
