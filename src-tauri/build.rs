fn main() {
    println!("cargo:rerun-if-env-changed=VPN_SERVER_URL");
    tauri_build::build()
}
