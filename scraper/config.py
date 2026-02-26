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

# Scraper Settings
RATE_INTERVAL = float(os.getenv("RATE_INTERVAL", "1.0"))

# Callback settings
CALLBACK_DETAIL_QUOTA = int(os.getenv("CALLBACK_DETAIL_QUOTA", "25"))   # detail 请求额度/turn
CALLBACK_GID_WINDOW = int(os.getenv("CALLBACK_GID_WINDOW", "10000"))      # 每轮跟进窗口: latest_gid - window
CALLBACK_RATING_DIFF_THRESHOLD = float(os.getenv("CALLBACK_RATING_DIFF_THRESHOLD", "0.5"))
CALLBACK_INLINE_SET = os.getenv("CALLBACK_INLINE_SET", "dm_l")

# Thumb downloader
THUMB_DIR = os.getenv("THUMB_DIR", "/data/thumbs")
THUMB_RATE_INTERVAL = float(os.getenv("THUMB_RATE_INTERVAL", "1.0"))

HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/124.0.0.0 Safari/537.36"
    ),
    "Accept-Language": "en-US,en;q=0.9",
    "Referer": EX_BASE_URL,
}
