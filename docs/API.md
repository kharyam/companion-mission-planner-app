# HTTP API reference

All endpoints are served from `http://<bind>:<port>` (default `127.0.0.1:8765`). Bodies are JSON unless noted. Errors use the envelope:

```json
{ "error": { "code": "DEVICE_NOT_AUTHORIZED", "message": "...", "details": {} } }
```

## Auth

If `auth.token` is set in config, every request must send one of:

- `Authorization: Bearer <token>`
- `X-KAM-Token: <token>`

Empty token disables auth (default).

## Endpoints

### `GET /api/health`

```json
{ "status": "ok", "version": "1.0.0", "time": "2026-05-22T10:30:00Z" }
```

### `GET /api/devices`

List connected DJI controllers and phones.

```json
{
  "devices": [
    {
      "id": "abc123",
      "model": "DJI RC 2",
      "connectionType": "adb",
      "authorized": true,
      "djiFlyDetected": true
    }
  ]
}
```

`authorized: false` means the user must approve USB debugging on the controller screen.

### `GET /api/devices/{deviceId}/slots`

Scan the waypoint folder and return all GUID-named slots.

```json
{
  "deviceId": "abc123",
  "slots": [
    {
      "guid": "550E8400-E29B-41D4-A716-446655440000",
      "name": "Slot 01 - Available",
      "lastModified": "2026-05-22T10:30:00Z",
      "fileSize": 4321,
      "previewAvailable": true,
      "previewUrl": "/api/devices/abc123/slots/550E8400.../preview"
    }
  ]
}
```

### `GET /api/devices/{deviceId}/slots/{guid}/preview`

Returns the slot's preview JPEG. `404` if no preview exists.

### `POST /api/devices/{deviceId}/slots/{guid}/transfer`

`multipart/form-data` with fields:

| Field            | Type     | Required | Notes |
|------------------|----------|----------|-------|
| `kmz`            | file     | yes      | Mission KMZ (max 10 MB) |
| `name`           | string   | no       | Display name for the mission |
| `previewMetadata`| JSON     | no       | `{ "waypoints": [{"lat":..,"lng":..}, ...], "center": {...} }` — triggers preview render |

Response:

```json
{ "success": true, "guid": "550E8400-...", "fileSize": 4321, "transferredAt": "2026-05-22T10:35:00Z" }
```

### `DELETE /api/devices/{deviceId}/slots/{guid}`

Marks the slot as available. See `TROUBLESHOOTING.md` for the placeholder strategy.

### `POST /api/devices/{deviceId}/refresh`

Re-scan the device. Use after the user creates new placeholder missions in DJI Fly.

### `GET /api/events` (WebSocket)

Server pushes JSON events:

```json
{ "type": "device.connected", "deviceId": "abc123", "at": "2026-05-22T10:30:00Z" }
{ "type": "device.disconnected", "deviceId": "abc123", "at": "..." }
{ "type": "device.authorized", "deviceId": "abc123", "at": "..." }
{ "type": "transfer.progress", "deviceId": "abc123", "slot": "...", "detail": { "percent": 42 } }
{ "type": "transfer.completed", "deviceId": "abc123", "slot": "...", "at": "..." }
```

## Error codes

| Code                    | HTTP | Meaning |
|-------------------------|------|---------|
| `BAD_REQUEST`           | 400  | Malformed request |
| `UNAUTHORIZED`          | 401  | Missing/invalid auth token |
| `DEVICE_NOT_FOUND`      | 404  | Unknown device ID |
| `DEVICE_NOT_AUTHORIZED` | 403  | USB debugging not yet approved |
| `SLOT_NOT_FOUND`        | 404  | Slot GUID doesn't exist |
| `INVALID_GUID`          | 400  | Malformed GUID parameter |
| `KMZ_INVALID`           | 400  | KMZ failed validation |
| `KMZ_TOO_LARGE`         | 413  | KMZ exceeds 10 MB cap |
| `TRANSFER_FAILED`       | 500  | Underlying transfer error |
| `DEVICE_DISCONNECTED`   | 503  | Device dropped mid-operation |
| `DEVICE_DISK_FULL`      | 507  | Out of space on device |
| `NOT_IMPLEMENTED`       | 501  | Feature not yet built |
| `INTERNAL_ERROR`        | 500  | Unclassified server error |
