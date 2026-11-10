# Hợp đồng giữa các thành phần

Tài liệu này chốt các giao diện giữa 4 người để làm song song. Mọi thay đổi ở đây
phải được cả D, N, T, L đồng ý. Đây là bản khởi đầu: các chỗ ghi **(cần xác nhận)** do
chủ thư mục kiểm tra trong mốc đầu rồi cập nhật lại tài liệu này.

Quy ước: tên kiểu, hàm, lệnh giữ nguyên tiếng Anh. Số nguyên nhiều byte dùng
**little-endian**, giống `src/comm`.

## 1. Tầng vận chuyển

Hub dùng lại đóng gói của croc (`src/comm`): mỗi gói là

```
"croc" (4 byte) | độ dài body u32 | body
```

Bước bắt tay với relay (PAKE với relay, mật khẩu relay, tên phòng) giữ nguyên từ
`clientHandshake` trong `src/tcp/tcp.go`. Sau khi bắt tay xong, nếu tên phòng bắt đầu
bằng `hub:` thì kết nối được giao cho hub thay vì `admitToRoom`. **(cần xác nhận:** cách
client suy ra tên phòng từ code phrase; D xác nhận ở M1.)

Sau khi vào phòng hub, `body` của mỗi gói là một **khung hub** (mục 2).

## 2. Khung hub

Header **không mã hóa** (hub đọc để định tuyến), payload là bản mã.

```
type    u8
channel u8
flags   u8
from    u16     (hub điền id người gửi, client không tự khai)
to      u16     (chỉ dùng với DIRECT; 0 = tất cả)
seq     u32     (tăng dần theo từng người gửi và từng kênh)
payload []byte  (bản mã, tối đa 64 KB cả khung)
```

### 2.1 Kiểu khung (`type`)

| Giá trị | Tên | Chiều | Ý nghĩa |
|---|---|---|---|
| 1 | `JOIN_ACK` | hub → client | Cấp `member id`, danh sách thành viên, ai là host |
| 2 | `MEMBER_JOINED` | hub → client | Có người vào |
| 3 | `MEMBER_LEFT` | hub → client | Có người ra |
| 4 | `DATA` | client → hub → tất cả trừ người gửi | Dữ liệu phát cho cả phòng |
| 5 | `DIRECT` | client → hub → một người | Gửi riêng (dùng cho PAKE và chuyển khóa) |
| 6 | `SNAPSHOT_PUT` | client → hub | Tải snapshot đã mã hóa lên |
| 7 | `SNAPSHOT` | hub → client | Hub trả snapshot cho người mới vào |
| 8 | `PING` | hai chiều | Giữ kết nối và đo độ trễ |
| 9 | `ERROR` | hub → client | Phòng đầy, bị giới hạn, khung quá lớn, ... (mã lỗi trong payload, không mã hóa) |

### 2.2 Kênh (`channel`) và `flags`

| Kênh | Dùng cho | Giao |
|---|---|---|
| 0 | Điều khiển | Tin cậy, đúng thứ tự |
| 1 | Nét vẽ, con trỏ | Tin cậy, đúng thứ tự |
| 2 | File | Tin cậy, đúng thứ tự |
| 3 | Âm thanh trực tiếp | Có thể bỏ (lossy) |
| 4 | Màn hình | Có thể bỏ (lossy) |

`flags`: bit 0 = `LOSSY` (hub được phép bỏ khung cũ nhất của người nhận đang chậm). Hub
áp `LOSSY` mặc định cho kênh 3 và 4; client không cần đặt.

### 2.3 Giới hạn (hub phải ép)

- Khung tối đa **64 KB**.
- Phòng tối đa **8** thành viên (cấu hình được).
- Snapshot tối đa **4 MB** (một bản mới nhất mỗi phòng).
- Người nhận chậm: hub có hàng đợi giới hạn cho mỗi người; kênh 0–2 đầy thì ngắt kết
  nối người đó với `ERROR`, kênh 3–4 thì bỏ khung cũ.
- Hub **bỏ qua giới hạn 30 lượt/phút** của `src/tcp/defaults.go` đối với phòng `hub:`
  (hoặc cấu hình riêng), để thử tải không bị chặn.

## 3. Mật mã

Dùng lại `src/pakekey` và `src/crypt`.

### 3.1 Vào phòng

1. Host tạo phòng, nhận `member id`, sinh `roomKey` ngẫu nhiên 32 byte.
2. Người mới nhập code phrase, vào phòng hub, nhận `JOIN_ACK` (biết host là ai).
3. Người mới gửi `DIRECT` tới host, kênh 0, loại `pake1` (SPAKE2, `pakekey.Init` với
   code phrase làm mật khẩu).
4. Host trả `pake2` bằng `DIRECT`; hai bên gọi `pakekey.Derive` để có `pairKey`, rồi
   xác nhận bằng `pakekey.Confirm` (thông điệp `pake-confirm`). Sai code phrase thì
   xác nhận thất bại và host từ chối.
5. Host gửi `roomKey` bằng `DIRECT`, loại `room-key`, mã hóa bằng `pairKey`.

Một phiên chỉ cho một lần thử đoán code phrase (tính chất của PAKE). Host nên giới hạn
số lần thử và số người đang chờ xác thực.

### 3.2 Mã hóa `DATA`

- AEAD (AES-256-GCM hoặc XChaCha20-Poly1305; T chọn và ghi vào đây khi làm xong M1b).
- **Nonce:** `member id (2 byte) | bộ đếm 64-bit tăng dần theo từng người gửi`, không
  bao giờ dùng lại với cùng `roomKey`.
- **Dữ liệu đi kèm (AAD):** toàn bộ header của khung.
- Khung không giải mã được thì client bỏ qua và ghi log, không làm hỏng phiên.

### 3.3 Đổi khóa khi có người rời

Phase 2, mức Should (T). Host sinh `roomKey` mới và gửi lại cho những người còn lại
bằng `DIRECT`, đánh số `keyEpoch`. Mã hóa phải có `keyEpoch` trong AAD khi tính năng này
được làm.

## 4. Thông điệp ứng dụng (nằm bên trong bản mã)

Điều khiển và bảng vẽ dùng JSON. Dữ liệu nhị phân (file, âm thanh, màn hình) dùng nhị
phân trực tiếp ở đầu payload sau khi giải mã. Mọi thông điệp có trường `t` (loại).

### 4.1 Kênh 0, điều khiển

```json
{"t":"hello","name":"Dat"}
{"t":"leave"}
{"t":"pake1","msg":"<base64>"}
{"t":"pake2","msg":"<base64>"}
{"t":"pake-confirm","tag":"<base64>"}
{"t":"room-key","key":"<base64 đã mã hóa>"}
```

### 4.2 Kênh 1, bảng vẽ

```json
{"t":"stroke","id":"<uuid>","ver":3,"tool":"pen","color":"#ff0000","width":4,
 "points":[[10,20],[12,24]]}
{"t":"delete","id":"<uuid>","ver":4}
{"t":"cursor","x":120,"y":80}
{"t":"snapshot","strokes":[ ... ]}
```

Quy tắc gộp: cùng `id` thì `ver` lớn hơn thắng; nếu bằng nhau thì thắng theo `member id`
nhỏ hơn. `delete` là một phiên bản của nét (xóa mềm). `cursor` là thông tin tạm, không
vào snapshot và có thể bỏ bớt.

### 4.3 Kênh 2, file

```json
{"t":"file-offer","id":"<uuid>","name":"report.pdf","size":123456,"sha256":"<hex>"}
{"t":"file-done","id":"<uuid>"}
```

Khối dữ liệu: `file-chunk` là nhị phân gồm `id (16 byte) | chỉ số khối u32 | dữ liệu
(tối đa 32 KB)`. Người nhận ghép lại theo chỉ số, kiểm tra `sha256`. File âm thanh là
file bình thường; app nhận biết bằng phần mở rộng/loại và cho phát.

### 4.4 Kênh 3, âm thanh trực tiếp (Phase 2)

Nhị phân: `timestamp_ms u32 | gói Opus`. Mỗi gói ~20 ms. Người nhận đệm vài khung để
chống giật.

### 4.5 Kênh 4, màn hình (Phase 2)

Nhị phân: `timestamp_ms u32 | khung hình` (JPEG hoặc khung nén ffmpeg; L chốt định dạng
và ghi vào đây).

## 5. API Go cho `src/room` (T cung cấp, N và L dùng)

```go
package room

type Channel uint8

const (
    ChControl Channel = 0
    ChDraw    Channel = 1
    ChFile    Channel = 2
    ChAudio   Channel = 3
    ChScreen  Channel = 4
)

type Member struct {
    ID     uint16
    Name   string
    IsHost bool
}

type Message struct {
    Channel Channel
    From    uint16
    Payload []byte // đã giải mã
}

type EventKind int

const (
    EventJoined EventKind = iota
    EventLeft
    EventClosed // phòng đóng (ví dụ host rời)
    EventError
)

type Event struct {
    Kind   EventKind
    Member Member // cho Joined/Left
    Err    error  // cho Error/Closed
}

type Options struct {
    Relay     string // "ip:9009"
    RelayPass string
    Name      string
}

type Session interface {
    Members() []Member
    Self() Member
    Send(ch Channel, payload []byte) error // phát cho cả phòng; payload chưa mã hóa
    Recv() <-chan Message
    Events() <-chan Event
    PutSnapshot(b []byte) error            // payload chưa mã hóa; T mã hóa trước khi gửi
    Snapshot() ([]byte, bool)              // snapshot nhận lúc vào (đã giải mã), nếu có
    Close() error
}

func Create(ctx context.Context, opts Options) (Session, string, error) // trả code phrase
func Join(ctx context.Context, code string, opts Options) (Session, error)
```

Quy tắc dùng:

- `Send` không chặn lâu; nếu hàng đợi đầy trả lỗi cho kênh tin cậy, bỏ khung cho kênh
  lossy.
- `Recv` và `Events` đóng lại khi `Close`.
- Gói `src/room/fake` có cùng `Create`/`Join` (qua một `Fake` hub trong bộ nhớ) để N và L
  phát triển mà không cần hub thật. T giao `room/fake` trước ngày thứ 3.

## 6. Binding Wails (N định nghĩa, phía JS gọi)

Phương thức Go (gọi từ giao diện):

| Phương thức | Mô tả |
|---|---|
| `CreateRoom(relay, pass, name) (code string)` | Tạo phòng, trả code phrase |
| `JoinRoom(relay, pass, name, code)` | Vào phòng |
| `Leave()` | Rời phòng |
| `SendStroke(json)` | Phát một nét vẽ/xóa |
| `SendCursor(x, y)` | Phát vị trí con trỏ |
| `SendFile(path)` | Gửi file (L cung cấp hàm thật phía dưới) |
| `SetMic(on bool)` / `SetScreenShare(on bool)` | Phase 2 |

Sự kiện Go → giao diện: `member` (`joined`/`left`), `stroke`, `cursor`, `file-offer`,
`file-progress`, `file-received`, `audio-level`, `room-closed`, `error`.

## 7. Dòng lệnh

```
croc-room host   --relay IP:9009 --pass P [--name N]      # in code phrase
croc-room join   CODE --relay IP:9009 --pass P [--name N]
croc-room chat                                            # gõ dòng, cả phòng thấy (sau host/join)
croc-room send   FILE                                     # gửi file vào phòng
croc-room members                                         # liệt kê thành viên
croc-relay-hub   --port 9009 --pass P [--max-members 8]    # D, hub chạy cùng relay
loadgen          --relay IP:9009 --pass P --code CODE --members 50   # D
media-tap        record --secs 5 --out a.opus | play a.opus          # L
```

`croc-room` cũng cần chạy được với `room/fake` bằng cờ `--fake` (T), để thử không cần
mạng.

## 8. Thay đổi hợp đồng

1. Mở một thay đổi trong `docs/contracts.md` (PR hoặc nhánh riêng).
2. Cả 4 người xem; nếu thay đổi chạm vào thư mục của ai thì người đó phải đồng ý.
3. Chủ thư mục cập nhật code/`fake` trong cùng ngày; ai dùng giao diện cũ báo lại.
