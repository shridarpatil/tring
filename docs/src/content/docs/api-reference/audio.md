---
title: Audio
description: Upload, list, play and delete sounds in the tenant's sound library, used for IVR prompts and hold music.
---

The sound library holds audio files you upload once and reuse anywhere a sound plays: IVR greetings, menus and other prompts, team queue music, and the tenant's [hold music](/tring/api-reference/tenant/#patch-v1tenantsme). See [Sounds & hold music](/tring/features/sounds-and-hold-music/).

Each sound has a `file_path`. That's the value you put in a flow node's audio file, a team's `moh_sound`, or the tenant's `hold_music`.

All endpoints accept a tenant API key or a console session.

### The audio object

| Field | Type | Description |
|---|---|---|
| `id` | UUID | Sound ID. |
| `tenant_id` | UUID | Owning tenant. |
| `name` | string | Display name, unique within the tenant. |
| `file_path` | string | Where the sound is stored on the server. Use this value to reference the sound elsewhere. |
| `format` | string | Stored format: `ogg` after a successful conversion, otherwise the uploaded file's extension (`wav`, `mp3`, `m4a`). |
| `size_bytes` | integer | Size of the stored file, in bytes. |
| `created_at` | timestamp | When the sound was uploaded. |

### POST /v1/audio

Upload a sound.

**Auth:** API key or console session.

The request is `multipart/form-data`, not JSON. Let your HTTP client set the `Content-Type` header with the multipart boundary.

**Form fields**

| Field | Type | Required | Description |
|---|---|---|---|
| `file` | file | yes | The audio file. The file name must end in `.wav`, `.mp3`, `.ogg` or `.m4a` (case doesn't matter). Maximum 10 MB. |
| `name` | string | no | Display name. Defaults to the file name without its extension. Must be unique within the tenant. |

```bash
curl -sS -X POST "$TRING_URL/v1/audio" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -F 'file=@welcome.wav' \
  -F 'name=Welcome greeting'
```

**Response** `201 Created`

```json
{
  "id": "7b0e9a52-0000-4000-8000-0000000000b1",
  "tenant_id": "00000000-0000-4000-8000-000000000001",
  "name": "Welcome greeting",
  "file_path": "/var/lib/tring/sounds/00000000-0000-4000-8000-000000000001/7b0e9a52-0000-4000-8000-0000000000b9.ogg",
  "format": "ogg",
  "size_bytes": 48213,
  "created_at": "2026-10-16T09:41:07.512843+05:30"
}
```

**How uploads are processed**

- Uploads are converted with `ffmpeg` to 48 kHz mono Opus in an Ogg container. That's the format the web-call media plane plays. The original is then discarded. `.ogg` uploads are stored as they are.
- A 30 ms fade is added at the start and end, so a clip used as hold music loops without a click.
- If `ffmpeg` isn't installed, or the conversion fails, the original file is kept and `format` is its extension. That file can still play on phone calls, but **won't play on web calls**. The API logs a warning when this happens.
- The file is stored under a generated name in your tenant's folder. The uploaded file name is used only to check the extension and set the default `name`.

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid upload or file too large (max 10MB)` |
| `400` | `file is required` |
| `400` | `only .wav, .mp3, .ogg, .m4a files are supported` |
| `400` | `upload failed or exceeds 10MB` |
| `409` | `a sound with this name already exists` |
| `500` | `storage unavailable`, `write failed`, `save failed` |
| `503` | `audio uploads are not configured` (the server has no audio directory set) |

### GET /v1/audio

List the tenant's sounds, newest first. Not paginated.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/audio" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `200 OK`

```json
[
  {
    "id": "7b0e9a52-0000-4000-8000-0000000000b1",
    "tenant_id": "00000000-0000-4000-8000-000000000001",
    "name": "Welcome greeting",
    "file_path": "/var/lib/tring/sounds/00000000-0000-4000-8000-000000000001/7b0e9a52-0000-4000-8000-0000000000b9.ogg",
    "format": "ogg",
    "size_bytes": 48213,
    "created_at": "2026-10-16T09:41:07.512843+05:30"
  }
]
```

**Errors**

| Status | `error` |
|---|---|
| `500` | `query failed`, `scan failed` |

### GET /v1/audio/{id}/file

Download a sound's audio, for example to play it in a browser.

**Auth:** API key or console session.

```bash
curl -sS "$TRING_URL/v1/audio/7b0e9a52-0000-4000-8000-0000000000b1/file" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  -o welcome.ogg
```

**Response** `200 OK` with the raw file. `Content-Type` is set from the file's extension. Range requests (`Range:` header, `206 Partial Content`) are supported, so the file can be streamed into an `<audio>` element.

To play it in a browser, fetch it with the `Authorization` header and use a blob URL. A plain `<audio src>` can't send the header.

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid id` |
| `404` | `sound not found` |
| `500` | `query failed` |

If the database entry exists but the file is missing from disk, you get a plain-text `404`, not JSON.

### GET /v1/audio/preview

Play one of your tenant's stored sounds by its `file_path`. This also works for sounds that aren't in the library, such as text-to-speech prompts generated when a flow is saved. Use it to preview whatever a flow node, team or the tenant's hold music currently points at.

**Auth:** API key or console session.

**Query params**

| Param | Required | Description |
|---|---|---|
| `path` | yes | The sound's `file_path`, URL-encoded. |

```bash
curl -sS -G "$TRING_URL/v1/audio/preview" \
  -H "Authorization: Bearer $TRING_API_KEY" \
  --data-urlencode 'path=/var/lib/tring/sounds/00000000-0000-4000-8000-000000000001/7b0e9a52-0000-4000-8000-0000000000b9.ogg' \
  -o preview.ogg
```

**Response** `200 OK` with the raw file, as for `GET /v1/audio/{id}/file`.

Only your own tenant's sounds (`<audio_dir>/<tenant-id>/…`) and text-to-speech prompts (`<audio_dir>/tts/…`) can be read. Paths into another tenant's folder, or that try to climb out with `../`, are refused. Stream URLs such as `local_stream://moh` aren't files and can't be previewed.

**Errors**

| Status | `error` |
|---|---|
| `400` | `not a previewable sound path` (empty `path`, a stream URL, a path outside your tenant's sounds and the TTS cache, or no audio directory configured) |
| `404` | `sound not found` |

### DELETE /v1/audio/{id}

Delete a sound and its file.

**Auth:** API key or console session.

```bash
curl -sS -X DELETE "$TRING_URL/v1/audio/7b0e9a52-0000-4000-8000-0000000000b1" \
  -H "Authorization: Bearer $TRING_API_KEY"
```

**Response** `204 No Content`

:::caution
Tring doesn't check whether a sound is still in use. If a flow node, a team's `moh_sound` or the tenant's `hold_music` still points at its `file_path`, that reference is left pointing at a file that no longer exists. Repoint them before deleting.
:::

**Errors**

| Status | `error` |
|---|---|
| `400` | `invalid id` |
| `404` | `sound not found` |
| `500` | `query failed`, `delete failed` |
