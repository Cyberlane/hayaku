// swift-tools-version: 6.0
import PackageDescription
let package = Package(name:"Fixture", targets:[.target(name:"Core",resources:[.copy("data/value.txt")]),.testTarget(name:"FixtureTests",dependencies:["Core"])])
