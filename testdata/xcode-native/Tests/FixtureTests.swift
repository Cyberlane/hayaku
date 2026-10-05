import XCTest
@testable import Fixture
final class FixtureTests: XCTestCase { func testNumber() { XCTAssertEqual(number(), 7) };func testSkip() throws { throw XCTSkip("fixture") } }
