# Các bước chạy thử multi-send qua relay Azure

Dùng cho ma trận MVP (`phan-cong.md`, mốc MVP) và mọi lần đo trên nhiều máy thật. Giả định
giao diện terminal (việc của T) đã xong: người gửi và người nhận tự in thời gian và tốc độ.
Kết quả ghi vào [`multi-e2e-test.md`](multi-e2e-test.md); **không có gì tự lưu lại**, và biểu đồ
của VM mất khi VM bị xóa.

## 1. Dựng relay (D)

```bash
cd deploy/terraform && terraform apply
terraform output -raw public_ip            # IP mới mỗi lần apply
```

1. Sửa `ansible_host=<IP>` trong `deploy/ansible/inventory.ini`.
2. Build và triển khai từ `main`:

```bash
git checkout main && git pull
deploy/build.sh
cd deploy/ansible && ansible-playbook playbooks/relay.yaml
```

3. Kiểm tra: `nc -vz <IP> 9009` và `nc -vz <IP> 5201` đều mở (`iperf3` chạy sẵn trên VM).
4. Gửi IP cho cả nhóm. Mật khẩu relay là `relay_password` trong `deploy/ansible/vars.yaml`; gửi
   riêng, không dán vào nhóm chung.

## 2. Mỗi người chuẩn bị

```bash
git checkout main && git pull
go build -o croc .
git log --oneline -1                       # cả nhóm phải cùng một commit
export CROC_PASS=<mật khẩu relay>          # thay cho --pass, không lộ trong lịch sử shell
sudo apt install iperf3                    # nếu chưa có
```

Đo đường truyền của chính mình tới relay và ghi lại:

```bash
iperf3 -c <IP> -t 5                        # tải lên
iperf3 -c <IP> -t 5 -R                     # tải xuống
```

## 3. Mỗi lượt gửi

Người gửi:

```bash
croc --relay <IP>:9009 send --receivers <N> FILE
```

Người nhận (mỗi người một thư mục trống, có sẵn):

```bash
mkdir -p ~/nhan-<lượt>
croc --relay <IP>:9009 --out ~/nhan-<lượt> --multi <CODE>
```

- Cờ toàn cục (`--relay`, `--out`, `--debug`) đứng **trước** `send` / `--multi`.
- Bỏ `--code` để croc tự sinh code ba từ; gửi code cho người nhận.
- Nếu `--timeout` chưa được sửa (`plan.md` mục 11) thì thêm `--timeout 3h` cho người gửi.
- Một máy chạy nhiều người nhận được, nhưng chung đường xuống: số đo đó không dùng để kết luận
  tốc độ.

Trong khi chạy, D mở Azure portal → VM → **Monitoring → Metrics**, chọn
**Network Out Total** và **Percentage CPU**. Chụp lại trước khi xóa VM. Network Out phẳng ở một
mức hoặc CPU gần 100% nghĩa là relay là chỗ nghẽn.

Sau mỗi lượt, mọi người nhận chạy `sha256sum <file>` và so với người gửi.

## 4. Ma trận

| Lượt | Người gửi | Người nhận | File |
|---|---|---|---|
| 1 | D | N, T, L | 100 MB |
| 2 | N | D, T, L | 100 MB |
| 3 | T | D, N, L | 1 GB |
| 4 | L | D, N, T | 1 GB |
| 5 | D | 8 người nhận: 4 máy × 2 terminal | 1 GB |

Tạo file thử: `head -c 100M /dev/urandom > 100m.bin`, `head -c 1G /dev/urandom > 1g.bin`.

**Đạt khi:** `sha256` khớp trên mọi máy; người gửi chỉ thoát sau khi mọi người nhận xong; số byte
tải lên ≈ kích thước file (dòng tóm tắt của người gửi), không nhân với N.

## 5. Thử lỗi

1. Tắt một người nhận (`Ctrl+C` hoặc tắt Wi‑Fi) ở khoảng 50%: những người khác vẫn nhận xong,
   người gửi báo người đó mất kết nối và thoát mã 1, máy đó không còn file dở.
2. Người thứ N+1 có code vào khi phòng đã đủ: bị từ chối `room-full`.
3. Người vào sau khi đã bắt đầu gửi: bị từ chối `room-locked`.
4. `croc --relay <IP>:9009 send FILE` / `croc --relay <IP>:9009 CODE` kiểu 1-1 vẫn chạy trên
   cùng relay.

## 6. Ghi kết quả

Mỗi người nhận một dòng trong `docs/multi-e2e-test.md`:

| Lượt | Người nhận | iperf3 lên / xuống (Mbit/s) | File | Thời gian (TUI) | Mbit/s (TUI) | `sha256` khớp |
|---|---|---|---|---|---|---|

Kèm cho mỗi lượt: dòng tóm tắt của người gửi (byte đã tải lên, thời gian, người chậm nhất) và
ảnh Network Out / CPU của VM.

## 7. Dọn dẹp (D)

1. Chép kết quả và ảnh biểu đồ vào `docs/multi-e2e-test.md` **trước**.
2. Rồi mới `cd deploy/terraform && terraform destroy` để tiết kiệm tín dụng.
