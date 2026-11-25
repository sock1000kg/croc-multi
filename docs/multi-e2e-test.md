# Kết quả thử multi-send đầu-cuối

Bảng kết quả của ma trận MVP (`phan-cong.md`, mốc MVP) và các lần đo trên nhiều máy thật.
Cách chạy từng bước: [`test-steps.md`](test-steps.md). **Chỉ ghi số đã đo thật**; ô chưa đo để
trống.

## Cách lấy số

Người gửi và người nhận đều in một dòng `bench:` ra stderr khi kết thúc (định dạng ở
`phan-cong.md`, mục "Giao diện terminal"):

```bash
croc --relay <IP>:9009 --name <tên> --out ~/nhan-<lượt> --multi <CODE> 2> lượt-<lượt>.log
grep '^bench:' lượt-<lượt>.log
sha256sum ~/nhan-<lượt>/<file>
```

```
bench: role=recv name=an-pc bytes=1485169722 seconds=61.90 mbps=191.9 result=done
```

- `seconds`: từ khung `DATA` đầu tiên tới khi mọi file khớp băm; không tính phòng chờ.
- `mbps` = `bytes × 8 / seconds / 1 000 000`, cùng đơn vị với `iperf3`.
- `result=failed`: `bytes` là số byte đã ghi trước khi lỗi.
- Người nhận nên đặt `--name` (không dấu cách) để người gửi và bảng này nhận ra ai là ai.

## Lần chạy

| Ngày | Commit (`git log --oneline -1`) | Relay | Ghi chú |
|---|---|---|---|
| | | | |

## Đường truyền mỗi máy tới relay

`iperf3 -c <IP> -t 5` (lên) và `iperf3 -c <IP> -t 5 -R` (xuống), đo trước khi chạy ma trận.

| Người | Máy / mạng | Lên (Mbit/s) | Xuống (Mbit/s) |
|---|---|---|---|
| D | | | |
| N | | | |
| T | | | |
| L | | | |

## Ma trận

| Lượt | Người gửi | Người nhận | File | Đạt |
|---|---|---|---|---|
| 1 | D | N, T, L | 100 MB | |
| 2 | N | D, T, L | 100 MB | |
| 3 | T | D, N, L | 1 GB | |
| 4 | L | D, N, T | 1 GB | |
| 5 | D | 8 người nhận: 4 máy × 2 terminal | 1 GB | |

**Đạt khi:** `sha256` khớp trên mọi máy; người gửi chỉ thoát sau khi mọi người nhận xong;
`ratio` của người gửi ≈ 1.00 (không nhân với số người nhận).

### Người gửi (dòng `bench: role=send`)

| Lượt | n | done | bytes | uploaded | ratio | seconds | mbps |
|---|---|---|---|---|---|---|---|
| 1 | | | | | | | |
| 2 | | | | | | | |
| 3 | | | | | | | |
| 4 | | | | | | | |
| 5 | | | | | | | |

### Người nhận (dòng `bench: role=recv`)

| Lượt | Người nhận (`name`) | bytes | seconds | mbps | result | `sha256` khớp |
|---|---|---|---|---|---|---|
| 1 | | | | | | |
| 1 | | | | | | |
| 1 | | | | | | |
| 2 | | | | | | |
| 2 | | | | | | |
| 2 | | | | | | |
| 3 | | | | | | |
| 3 | | | | | | |
| 3 | | | | | | |
| 4 | | | | | | |
| 4 | | | | | | |
| 4 | | | | | | |
| 5 | | | | | | |

Kèm ảnh **Network Out Total** và **Percentage CPU** của VM cho từng lượt (D chụp, xem
`test-steps.md` mục 3).

## Thử lỗi

| Bài thử | Kỳ vọng | Kết quả | Ghi chú |
|---|---|---|---|
| Tắt một người nhận ở ~50% | Người khác vẫn xong; người gửi báo người đó, thoát mã 1; máy đó không còn file dở, in `bench: ... result=failed` | | |
| Người thứ N+1 vào khi đủ người | Bị từ chối `room-full` | | |
| Vào sau khi đã bắt đầu gửi | Bị từ chối `room-locked` | | |
| Sai code | Người nhận báo lỗi xác thực; người khác không ảnh hưởng | | |
| `croc send FILE` / `croc CODE` 1-1 trên cùng relay | Vẫn chạy | | |

## Kết quả đã có

### M3: đo tốc độ qua Azure (21/11/2026)

Một laptop ở Việt Nam (WSL2) làm cả người gửi lẫn người nhận, relay `Standard_B1s` ở Azure
`southeastasia`, file ngẫu nhiên 300 MB, mỗi trường hợp 2 lần, `sha256` khớp ở mọi lần
(`scripts/bench/azure.sh`; phân tích ở `phan-cong.md`, mục M3).

| Đo | Lần 1 | Lần 2 |
|---|---|---|
| `iperf3`, 1 luồng TCP, lên / xuống | 296 / 297 Mbit/s | |
| `iperf3`, 4 luồng song song, lên | 531 Mbit/s | |
| croc 1-1 qua relay (4 kết nối dữ liệu) | 208 Mbit/s (12,1 s) | 168 Mbit/s (15,0 s) |
| multi-send, 1 người nhận (1 kết nối) | 187 Mbit/s (13,5 s) | 176 Mbit/s (14,3 s) |
| multi-send, 3 người nhận trên cùng máy | 58 Mbit/s mỗi người (43 s) | 25 Mbit/s mỗi người (99 s) |
| Số byte người gửi tải lên (mọi N) | 314 947 292 (1,0012 × file) | |

Số N=3 trên cùng một máy không dùng để kết luận tốc độ nhiều người nhận; số đó lấy từ ma trận ở
trên.

### Lượt chạy đầu qua Azure (21/11/2026)

1 người gửi (file zip 1,48 GB) → 2 người nhận. Người gửi chết giữa chừng do `--timeout` cắt cả
phiên (`plan.md` mục 11, đã sửa 23/11). Không có số đo; chạy lại bằng bản mới.
