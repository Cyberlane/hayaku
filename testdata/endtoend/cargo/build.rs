fn main() { println!("cargo:rerun-if-changed=generated-input.txt"); let v=std::fs::read_to_string("generated-input.txt").unwrap(); println!("cargo:rustc-env=FIXTURE_GENERATED={}",v.trim()); }
