"""TTS9000 - Telegram bot for article to audio conversion."""

import asyncio
import base64
import hashlib
import logging
import os
import sys
import time
from pathlib import Path
from urllib.parse import urlparse, urlunparse

import ffmpeg
import requests
from bs4 import BeautifulSoup
from ftfy import fix_encoding
from langdetect import LangDetectException, detect
from mistralai.client import Mistral
from telegram import Update
from telegram.ext import (
    Application,
    CommandHandler,
    MessageHandler,
    filters,
)


class TextExtractionError(Exception):
    """Error during text extraction."""


class TextCleaningError(Exception):
    """Error during text cleaning."""


class TTSGenerationError(Exception):
    """Error during TTS generation."""


DEFAULT_SYSTEM_PROMPT_CLEAN = (
    "Your job is to process text that will be fed to a text-to-speech model."
    "Remove all headers, footers, navigation menus, advertisements, image attributions "
    "and any non-article content from the following text. "
    "Return only the clean article text. "
    "Do not alter the text in any way (summarizing, adding parts, "
    "reformulating...):"
)

DEFAULT_SYSTEM_PROMPT_TITLE = (
    "Extract the main title or headline from the following article text. "
    "Return only the title:"
)




def extract_article_text(url):
    """Extract text content from a URL."""
    headers = {
        "User-Agent":
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
    }
    try:
        response = requests.get(url, headers=headers, timeout=120)
        response.raise_for_status()
        soup = BeautifulSoup(response.text, "html.parser")
        return soup.get_text(separator="\n", strip=True)
    except requests.exceptions.HTTPError as e:
        if e.response.status_code == 403:
            logger.warning("Cloudflare or similar block detected for %s", url)
            raise TextExtractionError(
                f"Access blocked (403) for {url}. "
                "Site may have Cloudflare or similar protection."
            ) from e
        if e.response.status_code >= 400 and e.response.status_code < 500:
            raise TextExtractionError(
                f"Client error ({e.response.status_code}) for {url}"
            ) from e
        if e.response.status_code >= 500:
            raise TextExtractionError(
                f"Server error ({e.response.status_code}) for {url}"
            ) from e
        raise TextExtractionError(
            f"HTTP error ({e.response.status_code}) for {url}"
        ) from e
    except requests.exceptions.RequestException as e:
        raise TextExtractionError(f"Request failed for {url}: {str(e)}") from e


def clean_text_with_mistral(text, api_key):
    """Clean text using Mistral AI."""
    client = Mistral(api_key=api_key, timeout_ms=300000)
    system_prompt = os.getenv("SYSTEM_PROMPT_CLEAN", DEFAULT_SYSTEM_PROMPT_CLEAN)
    prompt = f"{system_prompt}\n\n{text}"
    response = client.chat.complete(
        model="mistral-medium-latest",
        messages=[{"role": "user", "content": prompt}],
        temperature=0.1,
    )
    return response.choices[0].message.content


def detect_language(text):
    """Detect language of text."""
    try:
        return detect(text)
    except LangDetectException as e:
        logger.warning("Language detection failed: %s", str(e))
        return "en"


def generate_tts(text, api_key):
    """Generate TTS audio from text."""
    client = Mistral(api_key=api_key, timeout_ms=300000)
    language = detect_language(text)
    voice_id = "gb_jane_neutral" if language == "en" else "fr_marie_neutral"
    response = client.audio.speech.complete(
        model="voxtral-mini-tts-2603",
        input=text,
        voice_id=voice_id,
        response_format="mp3",
    )
    return base64.b64decode(response.audio_data)


def sanitize_url(url):
    """Sanitize URL by removing fragment, query, and trailing slash."""
    parsed = urlparse(url)
    clean = urlunparse(
        parsed._replace(fragment="", query="", path=parsed.path.rstrip("/"))
    )
    return clean


def get_cache_filename(url):
    """Get cache filename for a URL."""
    clean_url = sanitize_url(url)
    url_hash = hashlib.md5(clean_url.encode()).hexdigest()
    return f"generated/{url_hash}.mp3"




def process_url(url, api_key, raw_text):
    """Process URL and return audio data."""
    cache_dir = Path("generated")
    cache_dir.mkdir(exist_ok=True)
    cache_file = Path(get_cache_filename(url))

    if cache_file.exists():
        logger.info("Using cached file: %s", cache_file)
        return cache_file.read_bytes()

    try:
        clean_text = clean_text_with_mistral(raw_text, api_key)
    except Exception as e:
        raise TextCleaningError(f"Text cleaning failed: {str(e)}") from e
    try:
        audio_data = generate_tts(clean_text, api_key)
    except Exception as e:
        raise TTSGenerationError(f"TTS generation failed: {str(e)}") from e
    cache_file.write_bytes(audio_data)
    return audio_data


logging.basicConfig(
    level=logging.INFO, format="%(asctime)s - %(name)s - %(levelname)s - %(message)s"
)
logger = logging.getLogger(__name__)

# httpx logs every request URL at INFO, which would leak the bot token into logs.
logging.getLogger("httpx").setLevel(logging.WARNING)


async def start(update: Update, _):
    """Handle /start command."""
    await update.message.reply_text(
        "Send me a URL and I'll convert the article to audio!"
    )


def get_article_title(raw_text, api_key):
    """Extract article title from article text."""
    try:
        client = Mistral(api_key=api_key, timeout_ms=300000)
        system_prompt = os.getenv("SYSTEM_PROMPT_TITLE", DEFAULT_SYSTEM_PROMPT_TITLE)
        prompt = f"{system_prompt}\n\n{raw_text[:2000]}"
        response = client.chat.complete(
            model="mistral-small-latest",
            messages=[{"role": "user", "content": prompt}],
            temperature=0.1,
        )
        title = fix_encoding(response.choices[0].message.content.strip())
        return title if title else "article"
    except Exception as e:  # pylint: disable=broad-exception-caught
        logger.warning("Title extraction failed: %s", str(e))
        return "article"


def fix_audio_header(temp_file, cache_file):
    """Remux the audio to fix its MP3 header and return its duration."""
    (
        ffmpeg
        .input(temp_file)
        .output(cache_file, acodec='copy')
        .run(
            overwrite_output=True,
            capture_stdout=True,
            capture_stderr=True
        )
    )
    return int(float(ffmpeg.probe(cache_file)['format']['duration']))


def prune_cache(cache_dir, max_age_days):
    """Delete cached audio files older than max_age_days."""
    if not cache_dir.exists():
        return

    cutoff = time.time() - max_age_days * 86400
    for file in cache_dir.glob("*.mp3"):
        try:
            if file.stat().st_mtime < cutoff:
                file.unlink()
                logger.info("Pruned cache file: %s", file)
        except OSError as e:
            logger.warning("Failed to prune cache file %s: %s", file, e)


async def handle_url(update: Update, _):
    """Handle URL messages."""
    allowed_users = os.getenv("ALLOWED_USERS", "").split(",")
    logger.info(allowed_users)
    user_id = update.message.from_user.id or ""
    if allowed_users[0] != "" and str(user_id) not in allowed_users:
        logger.warning("Unauthorized access attempt by user: %s", user_id)
        await update.message.reply_text("🚫 You are not authorized to use this bot.")
        return
    url = update.message.text
    if not (url.startswith("http://") or url.startswith("https://")):
        await update.message.reply_text(
            "Please send a valid URL starting with http:// or https://"
        )
        return

    api_key = os.getenv("MISTRAL_API_KEY")
    if not api_key:
        await update.message.reply_text(
            "Server misconfigured. Please try again later."
        )
        return

    try:
        msg = await update.message.reply_text("🔍 Extracting text from webpage...")
        # Blocking calls (requests, Mistral, ffmpeg) run in a thread so the bot
        # stays responsive; the article is extracted once and reused throughout.
        raw_text = await asyncio.to_thread(extract_article_text, url)
        article_title = await asyncio.to_thread(get_article_title, raw_text, api_key)
        await msg.edit_text(f"🧹 Cleaning text for {article_title}...")
        audio_data = await asyncio.to_thread(process_url, url, api_key, raw_text)
        await msg.edit_text("🎤 Generating TTS...")
        cache_file = get_cache_filename(url)
        temp_file = cache_file + ".temp"
        await asyncio.to_thread(Path(temp_file).write_bytes, audio_data)
        await msg.edit_text("🔧 Fixing audio header...")
        try:
            duration = await asyncio.to_thread(fix_audio_header, temp_file, cache_file)
        except ffmpeg.Error as e:
            logger.error("ffmpeg error: %s", e.stderr.decode())
            raise
        logger.info("Audio duration: %ss", duration)
        await asyncio.to_thread(Path(temp_file).unlink)
        await msg.delete()
        await update.message.reply_audio(
            audio=Path(cache_file).read_bytes(),
            title=article_title,
            duration=duration
        )
    except (TextExtractionError, TextCleaningError, TTSGenerationError) as exc:
        logger.error("Error processing URL for Telegram: %s", str(exc))
        error_msg = str(exc)
        if "403" in error_msg or "blocked" in error_msg.lower():
            error_msg = (
                "🚫 Access blocked. This site may have Cloudflare or similar "
                "protection that prevents automated access."
            )
        await update.message.reply_text(error_msg)


def run_telegram_bot():
    """Run the Telegram bot."""
    token = os.getenv("TELEGRAM_BOT_TOKEN")
    if not token:
        print("TELEGRAM_BOT_TOKEN environment variable not set")
        sys.exit(1)

    max_age_days = int(os.getenv("CACHE_MAX_AGE_DAYS", "30"))
    prune_cache(Path("generated"), max_age_days)

    application = Application.builder().token(token).build()
    application.add_handler(CommandHandler("start", start))
    application.add_handler(
        MessageHandler(filters.TEXT & ~filters.COMMAND, handle_url)
    )
    application.run_polling()


def main():
    """Main entry point."""
    run_telegram_bot()


if __name__ == "__main__":
    main()
