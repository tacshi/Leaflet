import Foundation
import Testing

@testable import Leaflet

struct SVGPercentsTests {
    private func resolved(_ svg: String) -> String {
        String(decoding: SVGPercents.resolved(Data(svg.utf8)), as: UTF8.self)
    }

    @Test func aRectFillsTheViewport() {
        let svg = resolved(
            ##"<svg xmlns="http://www.w3.org/2000/svg" width="600" height="400" viewBox="0 0 600 400"><rect width="100%" height="100%" fill="#DDDDDD"/></svg>"##
        )
        #expect(svg.contains(##"<rect width="600.0" height="400.0" fill="#DDDDDD"/>"##))
    }

    @Test func theViewBoxComesBeforeWidthAndHeight() {
        let svg = resolved(
            #"<svg width="120" height="80" viewBox="0,0,60,40"><circle cx='50%' cy='25%' r='10%'/></svg>"#
        )
        #expect(svg.contains(#"cx='30.0' cy='10.0'"#))
        let diagonal = ((60.0 * 60 + 40 * 40) / 2).squareRoot()
        #expect(svg.contains("r='\(10.0 / 100 * diagonal)'"))
    }

    @Test func otherAttributesStay() {
        let svg =
            #"<svg viewBox="0 0 10 10"><rect stroke-width="50%" width="4"/><stop offset="50%"/></svg>"#
        #expect(resolved(svg) == svg)
    }

    @Test func noViewportLeavesItAsItIs() {
        let svg = #"<svg><rect width="100%"/></svg>"#
        #expect(resolved(svg) == svg)
    }
}
