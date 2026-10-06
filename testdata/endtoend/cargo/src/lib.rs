mod value;
pub fn shared() -> i32 { value::value()*2 }
#[cfg(test)] mod tests {
    #[test] fn direct() { assert_eq!(super::value::value(),7); }
    #[test] fn shared() { assert_eq!(super::shared(),14); }
    #[test] fn resource() { assert_eq!(std::fs::read_to_string("data/value.txt").unwrap(),"ready\n"); }
    #[test] fn generated_feature() { assert!(cfg!(feature="qualified-feature")); assert_eq!(env!("FIXTURE_GENERATED"),"7"); }
}
