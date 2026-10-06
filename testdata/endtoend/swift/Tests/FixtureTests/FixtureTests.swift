import XCTest
@testable import Core
final class FixtureTests: XCTestCase {
    override func setUpWithError() throws { XCTAssertEqual(7,7) }
    func testDirect() { XCTAssertEqual(Core.value(),7) }
    func testShared() { XCTAssertEqual(shared(),14) }
    func testResource() throws { XCTAssertEqual(try resource(),"ready\n") }
}
