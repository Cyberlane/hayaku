import Foundation
public func value() -> Int { 7 }
public func shared() -> Int { value()*2 }
public func resource() throws -> String { try String(contentsOf: Bundle.module.url(forResource:"value",withExtension:"txt")!,encoding:.utf8) }
