---
title: Sounds and hold music
description: Upload audio for IVR prompts and hold music, set tenant and team hold music, and use built-in text-to-speech.
---

Each tenant has a **sound library**: uploaded audio you reuse anywhere a sound plays, such as IVR prompts and hold music. Manage it in the console (**Sounds**) or with the [Audio API](/tring/api-reference/audio/).

## Uploading audio

```bash
curl -sS -X POST https://api.example.com/v1/audio \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -F "file=@welcome.mp3" \
  -F "name=Welcome"
```

| Rule | Value |
|---|---|
| Formats accepted | `.wav`, `.mp3`, `.ogg`, `.m4a` |
| Maximum size | 10 MB |
| `name` | Optional (defaults to the file name); unique per tenant |

On upload, the api converts the file to **48 kHz mono Opus in Ogg** with ffmpeg, which is what the gateway plays, and adds a 30 ms fade at both ends so the file loops without clicks. Files are stored under `TRING_AUDIO_DIR/<tenant-id>/` with a random file name.

:::caution
If ffmpeg is missing, the original file is stored as-is and will **not** play on web or phone calls through the gateway. The stock api image includes ffmpeg.
:::

The response includes the sound's `file_path`. That value is what goes into a flow node's `audio_file`, the tenant's `hold_music` and a team's `moh_sound`.

| Endpoint | What it does |
|---|---|
| `GET /v1/audio` | List sounds. |
| `GET /v1/audio/{id}/file` | Download or play a sound. |
| `GET /v1/audio/preview?path=<file_path>` | Play one of your sounds, or a synthesized prompt, by its path. |
| `DELETE /v1/audio/{id}` | Delete a sound and its file. Flows or settings that still point at it will play nothing. |

## Hold music

What a waiting or held caller hears is resolved in this order:

| Situation | Plays |
|---|---|
| Waiting for a **team** (queue or ring, including a blind transfer to a team) | The team's `moh_sound` if it is a sound file, else the tenant's hold music |
| Waiting for **one agent** or **all agents** (including a blind transfer to an agent) | The tenant's hold music |
| **Held** by an agent | The tenant's hold music |
| An IVR **transfer to a number** ringing | The tenant's hold music |

Set the tenant's hold music under **Settings**, or:

```bash
curl -sS -X PATCH https://api.example.com/v1/tenants/me \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"hold_music": "/var/lib/tring/sounds/<tenant-id>/<file>.ogg"}'
```

Send `""` to clear it. Set a team's music with `moh_sound` on `PATCH /v1/teams/{name}`.

:::note
The gateway plays only sound files. When no file is set, the default value `local_stream://moh` (FreeSWITCH's built-in music) means **silence** for calls on the gateway; it only plays for hold done on a FreeSWITCH leg through the REST hold endpoints. Upload a hold-music file and select it in Settings so callers never hear dead air.
:::

Hold music loops for as long as the caller waits.

## Text-to-speech

You do not need to record every prompt. Type the text into a node's prompt field, and when you save the flow the api speaks it with **Piper**, a local text-to-speech engine, and stores the audio as a normal sound file under `TRING_AUDIO_DIR/tts`. Calls then play that file. Text is never sent to a third-party service.

- Piper, a default English voice and ffmpeg ship in the api image; no setup is needed.
- Identical text is synthesized once and cached.
- Prompts containing variables (`{{…}}`) are not synthesized.
- The tenant's **transfer failed message** is synthesized the same way when you save it.
- An uploaded sound on a node always takes priority over its text.

To use a different voice, put a Piper `.onnx` model and its `.onnx.json` side by side, point `TRING_TTS_PIPER_MODEL` at the model, and set `TRING_TTS_VOICE` to a speaker id for multi-speaker models. Then re-save your flows so prompts are synthesized with the new voice. If Piper, the model or ffmpeg is missing, TTS turns off quietly and text-only prompts are silent. See [Configuration](/tring/getting-started/configuration/#6b-ivr-audio-library-and-text-to-speech) and [IVR flows](/tring/features/ivr-flows/#text-to-speech-prompts).
