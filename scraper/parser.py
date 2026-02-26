import re
from dataclasses import dataclass
from bs4 import BeautifulSoup, Tag

GID_TOKEN_RE = re.compile(r"/g/(\d+)/([a-f0-9]+)/")
NEXT_CURSOR_RE = re.compile(r'[?&]next=(\d+)')
RATING_RE = re.compile(r"([0-5](?:\.\d+)?)")
TAG_CLASS_RE = re.compile(r"^gt")
OPACITY_RE = re.compile(r"opacity\s*:\s*([0-9.]+)")
SPACE_RE = re.compile(r"\s+")


@dataclass(frozen=True)
class GalleryListItem:
    gid: int
    token: str
    title: str
    rating_sig: str
    rating_est: float | None
    visible_tag_count: int


def _normalize_text(value: str) -> str:
    return SPACE_RE.sub(" ", value or "").strip()


def _extract_rating_signal(element: Tag) -> tuple[str, float | None]:
    ir_node = element.find(class_=re.compile(r"\bir"))
    if ir_node:
        title = _normalize_text(ir_node.get("title", ""))
        if title:
            m = RATING_RE.search(title)
            if m:
                value = float(m.group(1))
                return f"avg:{value:.2f}", value
            return f"title:{title}", None

        style = _normalize_text(ir_node.get("style", ""))
        if style:
            m = OPACITY_RE.search(style)
            if m:
                try:
                    opacity = float(m.group(1))
                    # EX list stars often encode fill ratio in opacity; map 0..1 to 0..5.
                    estimate = max(0.0, min(5.0, opacity * 5.0))
                    return f"style:{style.replace(' ', '')}", estimate
                except ValueError:
                    pass
            return f"style:{style.replace(' ', '')}", None

        classes = [c for c in (ir_node.get("class") or []) if c]
        if classes:
            return f"class:{','.join(sorted(classes))}", None

    for klass in ("gl4e", "gl4t", "gl5t", "gl5m", "gl5c"):
        node = element.find(class_=klass)
        if not node:
            continue
        text = _normalize_text(node.get_text(" ", strip=True))
        m = RATING_RE.search(text)
        if m:
            value = float(m.group(1))
            return f"text:{value:.2f}", value

    return "", None


def _extract_visible_tags(element: Tag) -> set[str]:
    tags: set[str] = set()

    for node in element.find_all(True):
        classes = node.get("class") or []
        if not any(TAG_CLASS_RE.match(c) for c in classes):
            continue
        text = _normalize_text(node.get_text(" ", strip=True)).lower()
        if not text:
            continue
        if len(text) > 80:
            continue
        tags.add(text)

    if tags:
        return tags

    for a in element.find_all("a", href=True):
        href = a.get("href", "")
        if "f_search=" not in href:
            continue
        text = _normalize_text(a.get_text(" ", strip=True)).lower()
        if not text:
            continue
        if len(text) > 80:
            continue
        if text in {"archive download", "torrent download"}:
            continue
        tags.add(text)
    return tags


def parse_gallery_list(html: str) -> tuple[list[GalleryListItem], int | None]:
    """
    解析列表页 HTML
    返回 (items, next_gid)
      items    = [GalleryListItem(...), ...]
      next_gid = 下一页游标 GID，None 表示已到最后一页
    """
    soup = BeautifulSoup(html, "lxml")
    results: list[GalleryListItem] = []

    itg = soup.find(class_="itg")
    if not itg:
        return results, None

    for element in itg.find_all(recursive=False):
        glname = element.find(class_="glname")
        if not glname:
            continue

        # 找到 <a> 链接
        a = glname.find("a")
        if a is None:
            parent = glname.parent
            if parent and parent.name == "a":
                a = parent
        if a is None:
            continue

        href = a.get("href", "")
        m = GID_TOKEN_RE.search(href)
        if not m:
            continue
        gid, token = int(m.group(1)), m.group(2)

        # 取最深子节点的文本作为标题
        node = glname
        while True:
            tag_children = [c for c in node.children if isinstance(c, Tag)]
            if not tag_children:
                break
            node = tag_children[0]
        title = _normalize_text(node.get_text(" ", strip=True) if isinstance(node, Tag) else str(node))
        rating_sig, rating_est = _extract_rating_signal(element)
        visible_tags = _extract_visible_tags(element)

        results.append(
            GalleryListItem(
                gid=gid,
                token=token,
                title=title,
                rating_sig=rating_sig,
                rating_est=rating_est,
                visible_tag_count=len(visible_tags),
            )
        )

    # 从分页栏提取下一页游标
    # ExHentai 分页: id="dnext" 为"下一页"按钮，href 含 next=<gid>
    next_gid: int | None = None
    dnext = soup.find(id="dnext")
    if dnext:
        href = dnext.get("href", "")
        m = NEXT_CURSOR_RE.search(href)
        if m:
            next_gid = int(m.group(1))

    return results, next_gid


def parse_detail(html: str) -> dict:
    """
    解析画廊详情页 HTML，返回字段 dict。
    """
    soup = BeautifulSoup(html, "lxml")
    data: dict = {}

    gm = soup.find(class_="gm")
    if not gm:
        return data

    # 标题
    gn = gm.find(id="gn")
    data["title"] = gn.get_text(strip=True) if gn else ""
    gj = gm.find(id="gj")
    data["title_jpn"] = gj.get_text(strip=True) if gj else ""

    # 分类  .cn（彩色）或 .cs（灰色）
    ce = gm.find(class_="cn") or gm.find(class_="cs")
    data["category"] = ce.get_text(strip=True) if ce else ""

    # 上传者
    gdn = gm.find(id="gdn")
    data["uploader"] = gdn.get_text(strip=True) if gdn else ""

    # 评分
    rating_label = gm.find(id="rating_label")
    if rating_label:
        rtext = rating_label.get_text(strip=True)
        if "Not Yet Rated" not in rtext:
            idx = rtext.find(" ")
            if idx != -1:
                try:
                    data["rating"] = float(rtext[idx + 1:])
                except ValueError:
                    pass
    
    # 封面 URL
    gd1 = gm.find(id="gd1")
    if gd1:
        div = gd1.find("div")
        if div:
            style = div.get("style", "")
            m = re.search(r"url\((.+?)\)", style)
            if m:
                data["thumb"] = m.group(1).strip("'\"")

    # 详情表格 #gdd
    gdd = gm.find(id="gdd")
    if gdd:
        for tr in gdd.find_all("tr"):
            tds = tr.find_all("td")
            if len(tds) < 2:
                continue
            key = tds[0].get_text(strip=True)
            value = tds[1].get_text(strip=True)

            if key.startswith("Posted"):
                data["posted"] = value
            elif key.startswith("Language"):
                data["language"] = value
            elif key.startswith("Length"):
                idx = value.find(" ")
                if idx >= 0:
                    try:
                        data["pages"] = int(value[:idx].replace(",", ""))
                    except ValueError:
                        pass
            elif key.startswith("Favorited"):
                if value == "Never":
                    data["fav_count"] = 0
                elif value == "Once":
                    data["fav_count"] = 1
                else:
                    idx = value.find(" ")
                    if idx >= 0:
                        try:
                            data["fav_count"] = int(value[:idx].replace(",", ""))
                        except ValueError:
                            data["fav_count"] = 0
                            
    # Comment count
    cdiv = soup.find(id="cdiv")
    if cdiv:
        aall = cdiv.find(id="aall")
        if aall:
            m = re.search(r"(\d+)", aall.get_text())
            data["comment_count"] = int(m.group(1)) if m else len(cdiv.find_all(class_="c1"))
        else:
            data["comment_count"] = len(cdiv.find_all(class_="c1"))
    else:
        data["comment_count"] = 0

    # Tags extraction
    taglist = soup.find(id="taglist")
    if taglist:
        tags = {}
        # Each row in table inside taglist represents a namespace? Not always.
        # Actually structure is usually <table><tr><td>namespace:</td><td><div><a>tag</a></div>...</td></tr>...</table>
        for tr in taglist.find_all("tr"):
            tds = tr.find_all("td")
            if len(tds) < 2:
                continue
            # namespace like 'language:', 'female:', or empty for misc
            ns_text = tds[0].get_text(strip=True).rstrip(":")
            namespace = ns_text if ns_text else "misc"
            
            tag_values = []
            for div in tds[1].find_all("div"):
                a = div.find("a")
                if a:
                    t = a.get_text(strip=True)
                    # sometimes tags have space or _
                    tag_values.append(t)
            
            if tag_values:
                tags[namespace] = tag_values
        data["tags"] = tags

    return data
