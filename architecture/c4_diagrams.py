"""Generate macula-cli's two C4 diagrams into assets/.

Run:  python3 architecture/c4_diagrams.py

  c4-context.svg     Level 1: macula-cli, who runs it, and the Macula stations,
                     realm and mesh nodes it talks to
  c4-components.svg  Level 3: the commands and packages inside the binary
                     (cmd/macula-cli, internal/) and the macula-go packages under them

Colours are plain classes with a prefers-color-scheme: dark block, no CSS
custom properties (rsvg-convert renders those black), so the SVGs render the
same on GitHub, in IDE previews and through rsvg-convert. The drawing helpers,
colours and legend are mcl-fovea's (macula-services/mcl-fovea,
architecture/c4_diagrams.py). After a change, render each
(rsvg-convert -w 1400 assets/c4-context.svg -o /tmp/c.png) and look at it;
keep architecture/README.md's tables in step.
"""
import os
from xml.sax.saxutils import escape

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "assets")

SANS = '"Atkinson Hyperlegible", "Segoe UI", system-ui, -apple-system, Helvetica, Arial, sans-serif'
MONO = '"JetBrains Mono", ui-monospace, Menlo, Consolas, monospace'

# Element kinds, each (light fill, light stroke, dark fill, dark stroke).
KINDS = {
    "b-sys": ("#F5E9CC", "#A56F0A", "#3A2E13", "#E3AA3C"),   # in scope
    "b-per": ("#E3EBF2", "#3B5B7A", "#1D2A36", "#8EB4D8"),   # person
    "b-mac": ("#E6E4EE", "#5E5A78", "#26243A", "#A9A4C8"),   # Macula infrastructure
    "b-ext": ("#ECEFED", "#8A9791", "#1C2421", "#7D8B85"),   # external system
    "b-cmp": ("#FFFFFF", "#8A9791", "#171F1C", "#7D8B85"),   # component or container
}

STYLE = f"""<style>
.t-name {{ font-family: {SANS}; font-size: 15px; font-weight: 700; }}
.t-desc {{ font-family: {SANS}; font-size: 12.5px; }}
.t-rel {{ font-family: {SANS}; font-size: 11.5px; paint-order: stroke; stroke-width: 4px; stroke-linejoin: round; }}
.t-code {{ font-family: {MONO}; font-size: 13px; font-weight: 600; }}
.t-type {{ font-family: {MONO}; font-size: 11.5px; }}
.t-bound {{ font-family: {MONO}; font-size: 12px; font-weight: 600; }}
.t-leg {{ font-family: {SANS}; font-size: 12px; }}
.edge {{ fill: none; stroke-width: 1.25; }}
.edge-later {{ fill: none; stroke-width: 1.25; stroke-dasharray: 5 4; }}
.bg {{ fill: #FFFFFF; }}
""" + "".join(f".{k} {{ fill: {v[0]}; stroke: {v[1]}; stroke-width: {2 if k == 'b-sys' else 1.25}; }}\n" for k, v in KINDS.items()) + """\
.b-later { fill: none; stroke: #8A9791; stroke-width: 1.25; stroke-dasharray: 5 4; }
.bound { fill: none; stroke: #B3BDB8; stroke-width: 1; }
.head { fill: #3B5B7A; }
.t-name, .t-code { fill: #16201D; }
.t-type, .t-bound, .t-leg { fill: #66736E; }
.t-desc { fill: #34413C; }
.t-rel { fill: #66736E; stroke: #FFFFFF; }
.edge, .edge-later { stroke: #8A9791; }
.arrowhead { fill: #8A9791; }
@media (prefers-color-scheme: dark) {
.bg { fill: #171F1C; }
""" + "".join(f".{k} {{ fill: {v[2]}; stroke: {v[3]}; }}\n" for k, v in KINDS.items()) + """\
.b-later { stroke: #7D8B85; }
.bound { stroke: #4A5752; }
.head { fill: #8EB4D8; }
.t-name, .t-code { fill: #E6ECE9; }
.t-type, .t-bound, .t-leg { fill: #93A09A; }
.t-desc { fill: #C3CDC8; }
.t-rel { fill: #93A09A; stroke: #171F1C; }
.edge, .edge-later { stroke: #7D8B85; }
.arrowhead { fill: #7D8B85; }
}
</style>"""

DEFS = ('<defs><marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        'orient="auto-start-reverse"><path class="arrowhead" d="M0 0L10 5L0 10z"/></marker></defs>')

LEGEND = [("b-sys", "In scope"), ("b-per", "Person"), ("b-mac", "Macula infrastructure"),
          ("b-ext", "External system"), ("b-cmp", "Container or component"), ("b-later", "Planned, not built")]
BOUNDARY = ("bound", "Boundary")


class Diagram:
    def __init__(self, name, title, w, h):
        self.name, self.title, self.w, self.h, self.parts = name, title, w, h, []

    def text(self, cls, x, y, s, anchor=None, rotate=None):
        a = f' text-anchor="{anchor}"' if anchor else ""
        r = f' transform="rotate({rotate} {x} {y})"' if rotate is not None else ""
        self.parts.append(f'<text class="{cls}" x="{x}" y="{y}"{a}{r}>{escape(s)}</text>')

    def box(self, cls, x, y, w, h, name, kind, desc, person=False, code=False):
        self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" rx="10"/>')
        tx = x + 16
        if person:
            self.parts.append(f'<circle class="head" cx="{x + 26}" cy="{y + 24}" r="9"/>')
            tx = x + 44
        self.text("t-code" if code else "t-name", tx, y + 28, name)
        self.text("t-type", tx, y + 46, kind)
        for i, line in enumerate(desc):
            self.text("t-desc", x + 16, y + 70 + 16 * i, line)

    def bound(self, x, y, w, h, label):
        self.parts.append(f'<rect class="bound" x="{x}" y="{y}" width="{w}" height="{h}" rx="12"/>')
        self.text("t-bound", x + 16, y + 22, label)

    def edge(self, d, later=False):
        self.parts.append(f'<path class="{"edge-later" if later else "edge"}" d="{d}" marker-end="url(#a)"/>')

    def legend(self, y, boundary=True):
        x = 24
        for cls, label in LEGEND + ([BOUNDARY] if boundary else []):
            self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="22" height="14" rx="3"/>')
            self.text("t-leg", x + 30, y + 12, label)
            x += 34 + 6.6 * len(label)

    def write(self):
        svg = [(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" role="img" '
                f'aria-label="{escape(self.title)}">'), f"<title>{escape(self.title)}</title>", STYLE, DEFS,
               f'<rect class="bg" x="0" y="0" width="{self.w}" height="{self.h}"/>'] + self.parts + ["</svg>", ""]
        with open(os.path.join(OUT, self.name), "w") as f:
            f.write("\n".join(svg))


def context():
    d = Diagram("c4-context.svg", "macula-cli system context", 1000, 640)
    d.box("b-per", 40, 50, 270, 96, "Operator", "[Person]",
          ["Tests, monitors and uses the mesh;", "admits devices at the join URL"], person=True)
    d.box("b-ext", 365, 50, 270, 96, "Scripts and agents", "[External: callers]",
          ["Run commands with -json and parse", "one envelope, failures by kind"])
    d.box("b-mac", 690, 50, 270, 96, "macula-realm", "[Macula infrastructure]",
          ["realm.macula.io: join sessions,", "membership UCANs, person binding"])
    d.box("b-sys", 365, 210, 270, 120, "macula-cli", "[Go binary on macula-go v0.27.0]",
          ["A scriptable wire 1.0 client: link,", "call and serve, publish and watch,", "DHT, content, realm and person"])
    d.box("b-ext", 40, 430, 270, 120, "GitHub Releases", "[External system]",
          ["goreleaser archives for Linux, macOS", "and Windows, amd64 and arm64, with", "checksums.txt"])
    d.box("b-mac", 365, 430, 270, 120, "Macula stations, DHT", "[Macula infrastructure]",
          ["Each -seed host:port@<node_id>,", "pinned by the node_id it must prove;", "the DHT holds the signed records"])
    d.box("b-ext", 690, 430, 270, 120, "Providers and peers", "[External: mesh nodes]",
          ["Procedures it calls (mcl-echo/echo,", "any advertised one), callers of what", "it serves, nodes sharing content"])
    d.edge("M310 98H365"); d.text("t-rel", 337, 90, "writes", anchor="middle")
    d.edge("M175 50V24H825V50"); d.text("t-rel", 500, 18, "admits the device at the join URL, signed in by email", anchor="middle")
    d.edge("M175 146V260H365"); d.text("t-rel", 183, 200, "runs it by hand")
    d.edge("M500 146V210"); d.text("t-rel", 508, 176, "run with -json;"); d.text("t-rel", 508, 192, "exit 0, 1 or 2")
    d.edge("M635 250H825V146"); d.text("t-rel", 833, 178, "HTTPS join sessions"); d.text("t-rel", 833, 194, "(realm proof v2);"); d.text("t-rel", 833, 210, "membership and person"); d.text("t-rel", 833, 226, "bind over the mesh")
    d.edge("M500 330V430"); d.text("t-rel", 508, 366, "QUIC, handshake version 5,"); d.text("t-rel", 508, 382, "SecP384r1MLKEM1024;"); d.text("t-rel", 508, 398, "signed requests")
    d.edge("M635 300H825V430"); d.text("t-rel", 833, 366, "calls by direct dial,"); d.text("t-rel", 833, 382, "sealed to a KEM key")
    d.edge("M310 490H337V310H365"); d.text("t-rel", 329, 400, "install.sh / install.ps1", anchor="middle", rotate=-90)
    d.edge("M690 490H635"); d.text("t-rel", 662, 482, "serve", anchor="middle"); d.text("t-rel", 662, 510, "through", anchor="middle")
    d.legend(600, boundary=False)
    d.write()


def components():
    d = Diagram("c4-components.svg", "macula-cli components", 1000, 820)
    d.bound(16, 40, 968, 706, "macula-cli binary [Go 1.27, cmd/macula-cli, internal/]")
    d.box("b-sys", 32, 70, 936, 80, "main.go", "[command dispatch]",
          ["One command per word; -h exits 0, a malformed invocation 2, a failure 1; version, commit and date set by goreleaser."], code=True)
    d.bound(32, 174, 936, 312, "Commands [cmd/macula-cli, one file each]")
    d.box("b-cmp", 48, 206, 288, 126, "connect, dht", "[connect.go, dht.go]",
          ["Resolve a seed and link to it,", "refusing a station that does not", "prove the pinned node_id; verified", "records by key or by type"], code=True)
    d.box("b-cmp", 356, 206, 288, 126, "call, serve", "[call.go, serve.go]",
          ["Direct dial, sealed when the provider", "advertises a KEM key; -ucan-file.", "Serve: echo or -reply, -confidential,", "-require-member (UCAN chain)"], code=True)
    d.box("b-cmp", 664, 206, 288, 126, "pubsub, stream, content", "[pubsub.go, stream.go, content.go]",
          ["Publish, watch verified events;", "a streaming round trip between two", "fresh nodes; share a file, fetch", "an MCID, probe both"], code=True)
    d.box("b-cmp", 48, 348, 288, 126, "identity", "[identity.go]",
          ["This node's node_id, key id and", "profile; prove-ownership: a payload", "with an ownership proof v2"], code=True)
    d.box("b-cmp", 356, 348, 288, 126, "realm", "[realm.go]",
          ["join and status: the join session", "over HTTPS, realm proof v2;", "membership: the UCAN over the mesh"], code=True)
    d.box("b-cmp", 664, 348, 288, 126, "person", "[person.go]",
          ["init: person.key, never connects;", "join once as yourself; bind and", "unbind a client node over the mesh"], code=True)
    d.box("b-cmp", 32, 510, 300, 96, "mesh.go", "[meshFlags]",
          ["-seed host:port@node_id, -realm,", "-realm-key, -identity, -ephemeral"], code=True)
    d.box("b-cmp", 350, 510, 300, 96, "internal/report", "[package]",
          ["One result: a -json envelope or", "text; failures by a fixed kind"], code=True)
    d.box("b-cmp", 668, 510, 300, 96, "internal/wirevalue", "[package]",
          ["Payloads JSON to wire CBOR and", "back; {\"$bytes\"}, no booleans"], code=True)
    d.box("b-cmp", 32, 630, 300, 96, "internal/identitystore", "[package]",
          ["Loads or creates identity.key,", "owner-readable, admission puzzle"], code=True)
    d.box("b-mac", 350, 630, 618, 96, "macula-go v0.27.0", "[Macula SDK, wire 1.0]",
          ["pool, stationlink, handshake, identity, profile, cbor, record, ucan,",
           "devicerequest (realm proof v2), ownershipproof, manifest"])
    d.edge("M500 150V174"); d.text("t-rel", 508, 166, "dispatches")
    d.edge("M182 486V510"); d.text("t-rel", 190, 502, "joins the mesh")
    d.edge("M500 486V510"); d.text("t-rel", 508, 502, "every result")
    d.edge("M816 486V510"); d.text("t-rel", 824, 502, "payloads")
    d.edge("M182 606V630"); d.text("t-rel", 190, 622, "the key")
    d.edge("M332 560H341V678H350"); d.text("t-rel", 349, 622, "pool.Pool, pinned seeds")
    d.legend(780)
    d.write()


if __name__ == "__main__":
    for make in (context, components):
        make()
