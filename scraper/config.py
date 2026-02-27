import os
import sys
from dotenv import load_dotenv

# Load .env file
load_dotenv()

# Database
DATABASE_URL = os.getenv("DATABASE_URL")
if not DATABASE_URL:
    print("Error: DATABASE_URL is not set.")
    sys.exit(1)

# E-Hentai Configuration
EX_COOKIES_STR = os.getenv("EX_COOKIES", "")
EX_BASE_URL = os.getenv("EX_BASE_URL", "https://exhentai.org")

# Parse cookies string "k=v;k2=v2" into dict
COOKIES = {}
if EX_COOKIES_STR:
    for pair in EX_COOKIES_STR.split(";"):
        if "=" in pair:
            k, v = pair.split("=", 1)
            COOKIES[k.strip()] = v.strip()

# Proxy (optional — set if your IP is banned)
PROXY_URL = os.getenv("PROXY_URL", "")
PROXIES = {"http": PROXY_URL, "https": PROXY_URL} if PROXY_URL else None

# Thumb downloader
THUMB_DIR = os.getenv("THUMB_DIR", "/data/thumbs")

# 全局请求速率限制（主站 list/detail，单位：秒/请求）
RATE_INTERVAL = float(os.getenv("RATE_INTERVAL", "2.0"))

# Thumb 下载速率限制（图片 CDN，单位：秒/请求，独立限速）
THUMB_RATE_INTERVAL = float(os.getenv("THUMB_RATE_INTERVAL", "0.5"))

HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/124.0.0.0 Safari/537.36"
    ),
    "Accept-Language": "en-US,en;q=0.9",
    "Referer": EX_BASE_URL,
}
