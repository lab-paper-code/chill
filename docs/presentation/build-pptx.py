"""Build two decks from docs/presentation figures.

  chill-images.pptx  : one PNG per slide
  chill-native.pptx  : SVG elements converted to editable PowerPoint shapes

Run: python3 docs/presentation/build-pptx.py
"""
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path

from lxml import etree
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_ANCHOR, PP_ALIGN
from pptx.oxml.ns import qn
from pptx.util import Emu, Pt

HERE = Path(__file__).parent
SW, SH = 12192000, 6858000  # 16:9
FONT = "NanumSquare"

DECK = [
    ("CHILL", None),
    (None, "summary-onepage"),
    (None, "why-needed"),
    (None, "architecture"),
    (None, "real-cluster"),
    (None, "scheduler-logic"),
    (None, "scheduler-4-execute"),
    (None, "scheduler-4b-scaleup"),
    (None, "eval-setup"),
    (None, "eval-results"),
    (None, "achievements"),
    (None, "next-steps"),
    ("부록", None),
    (None, "k8s-architecture"),
    (None, "request-pipeline"),
    (None, "firstfloor-runtime"),
    (None, "scheduler-1-input"),
    (None, "scheduler-2-capacity"),
    (None, "scheduler-2-example"),
    (None, "scheduler-3-decision"),
    (None, "scheduler-3a-options"),
    (None, "scheduler-3b-plans"),
    (None, "scheduler-3c-score"),
    (None, "scheduler-3d-step"),
    (None, "scheduler-3e-time"),
    (None, "scheduler-3f-example"),
]
DIVIDER_SUB = {"CHILL": "이기종 엣지 장비 대상 에너지 인지 AI 추론 배치", "부록": "스케줄러 세부 · Kubernetes 구성 · 요청 처리"}


def new_prs():
    p = Presentation()
    p.slide_width, p.slide_height = SW, SH
    return p


def blank(prs):
    return prs.slides.add_slide(prs.slide_layouts[6])


def divider(prs, title):
    s = blank(prs)
    for txt, y, size, color in ((title, SH * 0.40, 54, "0F172A"), (DIVIDER_SUB[title], SH * 0.52, 22, "475569")):
        tb = s.shapes.add_textbox(0, int(y), SW, Emu(900000))
        para = tb.text_frame.paragraphs[0]
        para.alignment = PP_ALIGN.CENTER
        r = para.add_run()
        r.text = txt
        font(r, size, color, bold=size > 30)


def font(run, pt, color, bold=False):
    run.font.size = Pt(pt)
    run.font.bold = bold
    run.font.color.rgb = RGBColor.from_string(color)
    run.font.name = FONT
    rpr = run._r.get_or_add_rPr()
    for tag in ("a:ea", "a:cs"):
        el = rpr.find(qn(tag))
        if el is None:
            el = etree.SubElement(rpr, qn(tag))
        el.set("typeface", FONT)


def fit(w, h):
    """Scale and offset (EMU) to fit a w×h canvas in the slide."""
    k = min(SW / w, SH / h)
    return k, (SW - w * k) / 2, (SH - h * k) / 2


# ---------- deck 1: images ----------
def build_images():
    prs = new_prs()
    for title, name in DECK:
        if title:
            divider(prs, title)
            continue
        png = HERE / f"{name}.png"
        from PIL import Image
        w, h = Image.open(png).size
        k, ox, oy = fit(w, h)
        blank(prs).shapes.add_picture(str(png), int(ox), int(oy), int(w * k), int(h * k))
    prs.save(HERE / "chill-images.pptx")


# ---------- deck 2: native shapes ----------
NS = "{http://www.w3.org/2000/svg}"


def parse_css(svg):
    css = {}
    st = svg.find(f".//{NS}style")
    if st is not None and st.text:
        for name, body in re.findall(r"\.([\w-]+)\s*\{([^}]*)\}", st.text):
            css[name] = decl(body)
    return css


def decl(body):
    return {k.strip(): v.strip() for k, v in (p.split(":", 1) for p in body.split(";") if ":" in p)}


def style_of(el, inherited, css):
    st = dict(inherited)
    for c in (el.get("class") or "").split():
        st.update(css.get(c, {}))
    for k in ("fill", "stroke", "stroke-width", "stroke-dasharray", "opacity", "font-size", "font-weight",
              "text-anchor", "dominant-baseline", "marker-end", "marker-start"):
        if el.get(k) is not None:
            st[k] = el.get(k)
    st.update(decl(el.get("style") or ""))
    return st


def color(v):
    if not v or v == "none" or not v.startswith("#"):
        return None
    v = v[1:]
    return v * 2 if len(v) == 3 else v[:6]


def px(v, default=0.0):
    try:
        return float(str(v).replace("px", ""))
    except (TypeError, ValueError):
        return default


class Canvas:
    def __init__(self, slide, k, ox, oy):
        self.s, self.k, self.ox, self.oy = slide, k, ox, oy

    def X(self, x):
        return int(self.ox + x * self.k)

    def Y(self, y):
        return int(self.oy + y * self.k)

    def L(self, v):
        return max(int(v * self.k), 1)


def apply_line(shape, st, cv, markers):
    stroke = color(st.get("stroke"))
    ln = shape.line
    if not stroke:
        ln.fill.background()
        return
    ln.color.rgb = RGBColor.from_string(stroke)
    ln.width = cv.L(px(st.get("stroke-width"), 1))
    dash = st.get("stroke-dasharray")
    lnx = shape._element.spPr.find(qn("a:ln"))
    if dash and dash != "none":
        a = [px(x) for x in re.split(r"[ ,]+", dash.strip()) if x]
        preset = "sysDot" if a and a[0] <= 3 else "dash"
        etree.SubElement(lnx, qn("a:prstDash")).set("val", preset)
    for attr, tag in (("marker-start", "a:headEnd"), ("marker-end", "a:tailEnd")):
        m = re.search(r"#([\w-]+)", st.get(attr) or "")
        if m and m.group(1) in markers:
            e = etree.SubElement(lnx, qn(tag))
            e.set("type", "triangle")
            e.set("w", "med")
            e.set("len", "med")


def apply_fill(shape, st):
    f = color(st.get("fill", "#000"))
    if not f:
        shape.fill.background()
        return
    shape.fill.solid()
    shape.fill.fore_color.rgb = RGBColor.from_string(f)
    op = st.get("fill-opacity") or st.get("opacity")
    if op and px(op, 1) < 1:
        clr = shape.fill._xPr.find(qn("a:solidFill"))[0]
        etree.SubElement(clr, qn("a:alpha")).set("val", str(int(px(op) * 100000)))


def no_shadow(shape):
    """Drop the theme style reference; it adds a shadow and theme-coloured defaults."""
    st = shape._element.find(qn("p:style"))
    if st is not None:
        shape._element.remove(st)


def arc_points(x1, y1, rx, ry, phi, fa, fs, x2, y2, n=16):
    """Sample an SVG endpoint arc (no rotation in our figures)."""
    if rx == 0 or ry == 0:
        return [(x2, y2)]
    dx, dy = (x1 - x2) / 2, (y1 - y2) / 2
    lam = dx * dx / (rx * rx) + dy * dy / (ry * ry)
    if lam > 1:
        rx, ry = rx * math.sqrt(lam), ry * math.sqrt(lam)
    num = rx * rx * ry * ry - rx * rx * dy * dy - ry * ry * dx * dx
    den = rx * rx * dy * dy + ry * ry * dx * dx
    c = math.sqrt(max(num / den, 0)) * (-1 if fa == fs else 1)
    cx_, cy_ = c * rx * dy / ry, -c * ry * dx / rx
    cx, cy = cx_ + (x1 + x2) / 2, cy_ + (y1 + y2) / 2
    t1 = math.atan2((dy - cy_) / ry, (dx - cx_) / rx)
    t2 = math.atan2((-dy - cy_) / ry, (-dx - cx_) / rx)
    dt = t2 - t1
    if fs and dt < 0:
        dt += 2 * math.pi
    if not fs and dt > 0:
        dt -= 2 * math.pi
    return [(cx + rx * math.cos(t1 + dt * i / n), cy + ry * math.sin(t1 + dt * i / n)) for i in range(1, n + 1)]


def path_subpaths(d):
    toks = re.findall(r"[MmLlHhVvZzAa]|-?\d*\.?\d+(?:e-?\d+)?", d)
    i, cmd, x, y, sx, sy = 0, None, 0.0, 0.0, 0.0, 0.0
    subs, cur = [], None

    def num():
        nonlocal i
        i += 1
        return float(toks[i - 1])

    while i < len(toks):
        if re.match(r"[A-Za-z]", toks[i]):
            cmd = toks[i]
            i += 1
            if cmd in "Zz":
                if cur:
                    cur[1] = True
                x, y = sx, sy
                continue
        rel = cmd.islower()
        c = cmd.upper()
        if c == "M":
            nx, ny = num(), num()
            x, y = (x + nx, y + ny) if rel else (nx, ny)
            sx, sy = x, y
            cur = [[(x, y)], False]
            subs.append(cur)
            cmd = "l" if rel else "L"
        elif c == "L":
            nx, ny = num(), num()
            x, y = (x + nx, y + ny) if rel else (nx, ny)
            cur[0].append((x, y))
        elif c == "H":
            nx = num()
            x = x + nx if rel else nx
            cur[0].append((x, y))
        elif c == "V":
            ny = num()
            y = y + ny if rel else ny
            cur[0].append((x, y))
        elif c == "A":
            rx, ry, phi, fa, fs, nx, ny = (num() for _ in range(7))
            ex, ey = (x + nx, y + ny) if rel else (nx, ny)
            cur[0].extend(arc_points(x, y, abs(rx), abs(ry), phi, int(fa), int(fs), ex, ey))
            x, y = ex, ey
    return subs


def draw_path(cv, d, st, markers, offset):
    subs = path_subpaths(d)
    filled = color(st.get("fill", "#000")) is not None
    for pts, closed in subs:
        if len(pts) < 2:
            continue
        pts = [(cv.X(px_ + offset[0]), cv.Y(py_ + offset[1])) for px_, py_ in pts]
        fb = cv.s.shapes.build_freeform(pts[0][0], pts[0][1], scale=1.0)
        fb.add_line_segments(pts[1:], close=closed)
        shp = fb.convert_to_shape()
        apply_fill(shp, st if (filled and (closed or len(subs) == 1)) else {**st, "fill": "none"})
        apply_line(shp, st, cv, markers)
        no_shadow(shp)


def draw_rect(cv, el, st, markers, off):
    x, y = px(el.get("x")) + off[0], px(el.get("y")) + off[1]
    w, h = px(el.get("width")), px(el.get("height"))
    if w <= 0 or h <= 0:
        return
    rx = px(el.get("rx"))
    kind = MSO_SHAPE.ROUNDED_RECTANGLE if rx > 0 else MSO_SHAPE.RECTANGLE
    shp = cv.s.shapes.add_shape(kind, cv.X(x), cv.Y(y), cv.L(w), cv.L(h))
    if rx > 0:
        shp.adjustments[0] = min(rx / min(w, h), 0.5)
    apply_fill(shp, st)
    apply_line(shp, st, cv, markers)
    no_shadow(shp)
    shp.text_frame.text = ""


def draw_circle(cv, el, st, markers, off):
    cx, cy, r = px(el.get("cx")) + off[0], px(el.get("cy")) + off[1], px(el.get("r"))
    shp = cv.s.shapes.add_shape(MSO_SHAPE.OVAL, cv.X(cx - r), cv.Y(cy - r), cv.L(2 * r), cv.L(2 * r))
    apply_fill(shp, st)
    apply_line(shp, st, cv, markers)
    no_shadow(shp)


def draw_line(cv, el, st, markers, off):
    x1, y1 = px(el.get("x1")) + off[0], px(el.get("y1")) + off[1]
    x2, y2 = px(el.get("x2")) + off[0], px(el.get("y2")) + off[1]
    draw_path(cv, f"M{x1},{y1} L{x2},{y2}", {**st, "fill": "none"}, markers, (0, 0))


def draw_text(cv, el, st, off):
    txt = "".join(el.itertext()).strip()
    if not txt:
        return
    size = px(st.get("font-size"), 16)
    x, y = px(el.get("x")) + off[0], px(el.get("y")) + off[1]
    anchor = st.get("text-anchor", "start")
    middle = st.get("dominant-baseline") in ("middle", "central")
    cy = y if middle else y - 0.35 * size
    w = max(len(txt) * size * 1.1 + 40, 200)
    left = {"start": x, "middle": x - w / 2, "end": x - w}[anchor]
    h = size * 1.6
    tb = cv.s.shapes.add_textbox(cv.X(left), cv.Y(cy - h / 2), cv.L(w), cv.L(h))
    tf = tb.text_frame
    tf.word_wrap = False
    tf.auto_size = None
    tf.margin_left = tf.margin_right = tf.margin_top = tf.margin_bottom = 0
    tf.vertical_anchor = MSO_ANCHOR.MIDDLE
    p = tf.paragraphs[0]
    p.alignment = {"start": PP_ALIGN.LEFT, "middle": PP_ALIGN.CENTER, "end": PP_ALIGN.RIGHT}[anchor]
    r = p.add_run()
    r.text = txt
    bold = str(st.get("font-weight", "400")) in ("bold", "700", "800", "900")
    font(r, size * cv.k / 12700, color(st.get("fill", "#000")) or "000000", bold)


def walk(cv, el, st, css, markers, off):
    tag = el.tag.replace(NS, "")
    if tag in ("defs", "style", "marker", "title"):
        return
    st = style_of(el, st, css)
    if tag == "g":
        m = re.search(r"translate\(\s*([-\d.]+)[ ,]+([-\d.]+)", el.get("transform") or "")
        o = (off[0] + float(m.group(1)), off[1] + float(m.group(2))) if m else off
        for ch in el:
            walk(cv, ch, st, css, markers, o)
        return
    if tag == "text":
        draw_text(cv, el, st, off)
        return
    shape_st = {k: v for k, v in st.items() if k not in ("font-size",)}
    if tag == "rect":
        draw_rect(cv, el, shape_st, markers, off)
    elif tag == "circle":
        draw_circle(cv, el, shape_st, markers, off)
    elif tag == "line":
        draw_line(cv, el, shape_st, markers, off)
    elif tag == "path":
        draw_path(cv, el.get("d"), shape_st, markers, off)


def svg_slide(prs, name):
    svg = ET.parse(HERE / f"{name}.svg").getroot()
    _, _, w, h = (float(v) for v in svg.get("viewBox").split())
    k, ox, oy = fit(w, h)
    cv = Canvas(blank(prs), k, ox, oy)
    css = parse_css(svg)
    markers = {m.get("id") for m in svg.iter(f"{NS}marker")}
    root_st = {"fill": "#000"}
    for ch in svg:
        walk(cv, ch, root_st, css, markers, (0.0, 0.0))


def build_native():
    prs = new_prs()
    for title, name in DECK:
        if title:
            divider(prs, title)
        else:
            svg_slide(prs, name)
    prs.save(HERE / "chill-native.pptx")


if __name__ == "__main__":
    build_images()
    build_native()
    print("ok")
