# Tugas Besar IF3110 — Database Management Platform

**Kelompok K02-09 · Milestone 1 · Atlas**

Atlas adalah platform administrasi database untuk mengelola koneksi, memberikan akses melalui group, menjelajahi schema dan data, serta menjalankan migration SQL dengan pencatatan riwayat. Backend Go menyajikan HTML, CSS bersama, dan vanilla JavaScript tanpa framework frontend.

Database PostgreSQL internal menyimpan identitas, group, keanggotaan, grant, dan konfigurasi koneksi. Database target menyimpan data aplikasi serta history migration yang dilindungi privilege terpisah. Dukungan utama memakai PostgreSQL; MariaDB, analisis execution plan PostgreSQL, dan schema diff tersedia dengan batas yang dijelaskan di bawah.

## Daftar isi

- [Prasyarat](#prasyarat)
- [Pemisahan database dan akun](#pemisahan-database-dan-akun)
- [Setup dari checkout bersih](#setup-dari-checkout-bersih)
- [Setup PostgreSQL manual](#setup-postgresql-manual)
- [Langkah awal penggunaan](#langkah-awal-penggunaan)
- [Dokumentasi halaman](#dokumentasi-halaman)
- [Kebijakan akses dan session](#kebijakan-akses-dan-session)
- [Migration dan integritas history](#migration-dan-integritas-history)
- [Batas viewer dan representasi data](#batas-viewer-dan-representasi-data)
- [Bonus dan batas dukungan](#bonus-dan-batas-dukungan)
- [Impor instalasi lama](#impor-instalasi-lama)
- [Pengujian](#pengujian)
- [Teknologi dan struktur proyek](#teknologi-dan-struktur-proyek)
- [Identitas kelompok](#identitas-kelompok)

## Prasyarat

| Kebutuhan | Versi atau keterangan |
| --- | --- |
| Git | Untuk mengambil repository. |
| Go | **1.25.0 atau lebih baru**, sesuai `go.mod`; implementasi menggunakan `crypto/pbkdf2`. |
| PostgreSQL | Image development **17-alpine** pada Compose. Gunakan PostgreSQL 17 untuk mengikuti setup ini; versi minimum server yang lebih lama belum ditetapkan melalui pengujian kompatibilitas. |
| Docker dan Docker Compose | Compose v2 dengan dukungan `up --wait`, bila memakai jalur Docker. Docker Desktop dapat digunakan pada Windows/macOS. |
| Python | Python 3, tersedia sebagai `python3` untuk script shell pembuat `.env`. |
| Browser | Browser modern dengan JavaScript, cookie, dan Fetch API. |
| Node.js | Opsional untuk menjalankan `node --check`; bukan dependency runtime aplikasi. |
| MariaDB | Opsional; Compose menggunakan **11.4** untuk bonus DBMS. |
| Windows | PowerShell dan Git Bash dengan `bash`, `sh`, serta `python3` di PATH; alternatifnya WSL dengan perintah Linux. |

Akses jaringan diperlukan saat pertama kali mengunduh module Go dan image Docker. Akun DBA dipakai untuk provisioning, bukan sebagai akun executor SQL pengguna.

## Pemisahan database dan akun

| Komponen | Contoh development | Isi dan akun |
| --- | --- | --- |
| Database internal | PostgreSQL `atlas_internal` | Schema `platform`: user, group, membership, grant, dan konfigurasi. Diakses server sebagai `atlas_internal`. |
| Database target | PostgreSQL `atlas_target` | Data yang dijelajahi dan dimigrasikan. Executor `atlas_executor` dan pencatat `atlas_audit` merupakan dua akun terpisah. |
| History PostgreSQL | Schema `platform_migrations` di database target | Tabel percobaan privat, view history, dan fungsi penyelesaian. Bukan tabel di database internal. |
| History MariaDB | Database `atlas_history_<hash>` pada instance target | `<hash>` adalah 16 digit awal SHA-256 nama database target. |
| Database pengujian | Database kontrol `atlas_test_control` pada cluster disposable | Helper test membuat database dan role unik bernama sementara, kemudian membersihkan objek yang dibuatnya. Jangan menunjuk data produksi. |

Database internal tidak boleh didaftarkan sebagai target, termasuk melalui alias host yang menunjuk instance dan database yang sama. Nama akun/database di README ini adalah contoh lokal; screenshot memakai data inventaris fiktif dan akun demo sementara yang tidak dibuat otomatis oleh instalasi biasa.

## Setup dari checkout bersih

### 1. Ambil source

URL berikut sesuai remote `origin` proyek:

```bash
git -c core.autocrlf=false clone https://github.com/EliWidjojo/IF3110-K02-09.git
cd IF3110-K02-09
```

Perintah tersebut juga dapat dipakai di PowerShell. Menonaktifkan konversi CRLF saat clone menjaga script `.sh` tetap dapat dijalankan dalam container Linux.

### 2. Buat environment, database, dan history

**macOS/Linux (bash/zsh):**

```bash
sh scripts/dev-setup.sh
# Opsional, sekaligus menyiapkan MariaDB:
# sh scripts/dev-setup.sh --mariadb
```

Script memanggil `scripts/dev-env.sh`, membuat `.env` dengan secret acak bila file belum ada, menjalankan service PostgreSQL sampai healthcheck siap, lalu menjalankan `go run ./cmd/target-setup`. `.env` yang sudah ada tidak ditimpa. Role, database internal, target, dan database kontrol test dibuat oleh script init Compose ketika volume masih kosong.

**Windows (PowerShell + Git Bash):**

Jalankan dari root repository. Padanan berikut memakai generator environment yang sama; `bash` dan `python3` harus dapat ditemukan melalui Git Bash.

```powershell
bash scripts/dev-env.sh
if ($LASTEXITCODE -ne 0) { throw 'Gagal membuat environment' }

# Muat format KEY=value yang dihasilkan dev-env.sh, bukan mengeksekusi isi file.
Get-Content .env | ForEach-Object {
    if ($_ -match '^([A-Z_][A-Z0-9_]*)=(.*)$') {
        [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2], 'Process')
    }
}

docker compose up -d --wait postgres
if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL belum siap' }
$env:TARGET_DRIVER = 'postgres'
$env:TARGET_EXECUTOR_ROLE = 'atlas_executor'
$env:TARGET_AUDIT_DSN = "postgres://atlas_audit:$($env:TARGET_AUDIT_PASSWORD)@127.0.0.1:55432/atlas_target?sslmode=disable"
go run ./cmd/target-setup
if ($LASTEXITCODE -ne 0) { throw 'Setup history gagal' }
Remove-Item Env:TARGET_AUDIT_DSN
```

Untuk MariaDB opsional, lanjutkan di PowerShell yang sama:

```powershell
docker compose --profile bonus up -d --wait mariadb
if ($LASTEXITCODE -ne 0) { throw 'MariaDB belum siap' }
$sha = [System.Security.Cryptography.SHA256]::Create()
$bytes = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes('atlas_target'))
$history = 'atlas_history_' + ([BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant()).Substring(0, 16)
$sha.Dispose()
$env:TARGET_DRIVER = 'mariadb'
$env:TARGET_DATABASE_NAME = 'atlas_target'
$env:TARGET_EXECUTOR_ROLE = 'atlas_executor@%'
$env:TARGET_AUDIT_DSN = "atlas_audit:$($env:TARGET_AUDIT_PASSWORD)@tcp(127.0.0.1:53307)/$history"
go run ./cmd/target-setup
if ($LASTEXITCODE -ne 0) { throw 'Setup history MariaDB gagal' }
Remove-Item Env:TARGET_AUDIT_DSN
```

Compose memakai volume persisten. Inisialisasi role hanya berjalan pada volume baru; mengganti password di `.env` tidak otomatis mengganti password role pada volume lama. Gunakan provisioning manual untuk instalasi existing. Jangan menjalankan `docker compose down -v` untuk instalasi yang datanya ingin dipertahankan.

### 3. Periksa konfigurasi lokal

`.env` diabaikan Git. Generator menerapkan izin `0600` pada sistem yang mendukungnya; pada Windows batasi akses file melalui izin akun/ACL setempat. Buka `.env` melalui editor lokal untuk melihat username dan password bootstrap, tanpa menyalinnya ke log atau repository. Tidak ada akun demo dengan password tetap.

| Environment | Fungsi |
| --- | --- |
| `PLATFORM_DATABASE_DSN` | Koneksi PostgreSQL internal. Generator menunjuk `atlas_internal` pada `127.0.0.1:55432`. Wajib untuk server. |
| `DATABASE_CREDENTIALS_KEY` | Base64 dari 32 byte acak untuk enkripsi password koneksi; wajib dan harus tetap sama setelah kredensial tersimpan. |
| `BOOTSTRAP_ADMIN_USERNAME`, `BOOTSTRAP_ADMIN_PASSWORD` | Membuat admin hanya jika tabel user masih kosong. Generator memakai username `admin` dan password acak. |
| `APP_ENV` | `development` untuk lokal; `production` mengaktifkan cookie Secure dan memerlukan origin HTTPS. |
| `APP_ORIGIN` | Origin browser yang diizinkan, default `http://localhost:8080`. Harus sesuai alamat yang digunakan saat membuka aplikasi. |
| `HTTP_ADDR` | Alamat bind HTTP server, default `127.0.0.1:8080`. |
| `POSTGRES_ADMIN_PASSWORD`, `MARIADB_ADMIN_PASSWORD` | Akun provisioning container, bukan akun executor aplikasi. |
| `INTERNAL_DATABASE_PASSWORD` | Password role internal pada inisialisasi Compose. |
| `TARGET_EXECUTOR_PASSWORD`, `TARGET_AUDIT_PASSWORD` | Password dua role target pada inisialisasi Compose. |
| `IMPORT_LEGACY_IDENTITY`, `IMPORT_LEGACY_DATABASES` | Opsional, hanya untuk impor instalasi lama yang diaktifkan eksplisit. |
| `TEST_PLATFORM_DSN`, `TEST_MARIADB_ADMIN_DSN` | Opsional, hanya untuk test integrasi pada cluster disposable. |

Untuk setup manual, salin `.env.example` dan isi seluruh nilai yang dibutuhkan. Kunci enkripsi acak dapat dibuat lokal dengan perintah berikut; keluaran adalah secret baru, bukan nilai untuk disalin ke README:

```bash
python3 -c "import base64,secrets; print(base64.b64encode(secrets.token_bytes(32)).decode())"
```

Di PowerShell gunakan `python` sebagai pengganti `python3` bila itu nama interpreter pada instalasi Anda. Jangan mengganti `DATABASE_CREDENTIALS_KEY` setelah menyimpan koneksi tanpa backup dan prosedur enkripsi ulang. Mengubah environment bootstrap setelah instalasi tidak mereset password user. Server menolak `PQGO_DEBUG=1` agar debug protokol database tidak aktif.

### 4. Jalankan server

**macOS/Linux:**

```bash
set -a
. ./.env
set +a
go run ./cmd/server
```

**PowerShell:** jika membuka terminal baru, muat ulang `.env` dengan blok `Get-Content` pada langkah 2, lalu:

```powershell
go run ./cmd/server
```

Buka [http://localhost:8080/](http://localhost:8080/) untuk login. Aplikasi tersedia pada `/platform/`; `GET /healthz` memberikan status HTTP, bukan pemeriksaan menyeluruh seluruh koneksi target. Proses Go tidak memuat `.env` sendiri, sehingga environment harus dimuat pada shell sebelum menjalankannya.

Untuk production, gunakan `APP_ENV=production`, `APP_ORIGIN` HTTPS, dan terminasi TLS/reverse proxy yang sesuai. HTTP server dapat tetap bind localhost di belakang proxy. Gunakan TLS database terverifikasi pada jaringan yang tidak tepercaya; `sslmode=disable` di contoh ini hanya untuk development lokal.

## Setup PostgreSQL manual

Sebagai DBA, buat role dan database berikut. Tentukan password memakai `\password` interaktif, bukan literal password dalam perintah SQL atau shell history.

```sql
CREATE ROLE atlas_internal LOGIN;
CREATE ROLE atlas_executor LOGIN;
CREATE ROLE atlas_audit LOGIN;
\password atlas_internal
\password atlas_executor
\password atlas_audit
CREATE DATABASE atlas_internal OWNER atlas_internal;
CREATE DATABASE atlas_target;
GRANT CREATE ON DATABASE atlas_target TO atlas_audit;
\connect atlas_target
ALTER SCHEMA public OWNER TO atlas_executor;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
```

Ketiga role tidak diberi SUPERUSER, CREATEDB, CREATEROLE, REPLICATION, BYPASSRLS, atau keanggotaan role tambahan. Pemilik database target tetap DBA. Untuk database existing, berikan executor kepemilikan/privilege hanya pada objek aplikasi yang memang boleh dimigrasikan. Jangan menggunakan akun platform internal sebagai executor.

Isi salinan `.env.example` secara lokal: `PLATFORM_DATABASE_DSN` menunjuk `atlas_internal`, kunci enkripsi acak, origin, dan bootstrap admin. DSN internal dan password tidak dikirim ke browser. Schema internal dipasang secara idempoten dengan kunci transaksi saat startup.

Siapkan history menggunakan akun `atlas_audit` yang terhubung ke **database target yang sama**. Masukkan DSN secara lokal tanpa mencetak atau memasukkannya sebagai argumen command line:

```sh
export TARGET_DRIVER=postgres TARGET_EXECUTOR_ROLE=atlas_executor
# Pada bash/zsh: masukkan DSN akun pencatat secara tersembunyi.
read -rs TARGET_AUDIT_DSN
export TARGET_AUDIT_DSN
go run ./cmd/target-setup
unset TARGET_AUDIT_DSN
```

Perintah ini membuat schema `platform_migrations`, tabel percobaan privat, view history, dan fungsi penyelesaian yang dibatasi capability. Executor mendapat USAGE schema, SELECT view publik history, serta EXECUTE satu fungsi; **tidak** mendapat privilege tabel percobaan, sequence, atau CREATE dalam schema history. Runtime memverifikasi pemilik dan privilege sebelum migration dan menolak konfigurasi yang tidak aman.

Untuk memasukkan DSN pencatat secara tersembunyi pada PowerShell:

```powershell
$env:TARGET_DRIVER = 'postgres'
$env:TARGET_EXECUTOR_ROLE = 'atlas_executor'
$secret = Read-Host 'DSN akun pencatat pada database target' -AsSecureString
$env:TARGET_AUDIT_DSN = [System.Net.NetworkCredential]::new('', $secret).Password
go run ./cmd/target-setup
Remove-Item Env:TARGET_AUDIT_DSN
$secret.Dispose()
```

Pada setup manual, DSN internal dan origin harus menunjuk instance lokal yang benar. Jika password memuat karakter khusus, gunakan encoding URL yang sesuai untuk DSN PostgreSQL. Password token alfanumerik dari generator development tidak memerlukan penanganan karakter khusus ini.

## Langkah awal penggunaan

1. Login menggunakan akun bootstrap yang dibuat lokal. Buka **Koneksi** pada navbar, lalu isi nama tampilan, DBMS, host, port, database, executor, akun pencatat history, password, dan mode TLS.
2. Untuk target PostgreSQL Compose, gunakan host `127.0.0.1`, port `55432`, database `atlas_target`, executor `atlas_executor`, dan pencatat `atlas_audit`. Password masing-masing berasal dari `.env` lokal. Untuk MariaDB Compose, gunakan driver MariaDB dan port `53307`.
3. Simpan konfigurasi dan gunakan **Periksa** untuk menguji koneksi executor. Setup history harus sudah dijalankan melalui `cmd/target-setup`; pemeriksaan privilege history yang lebih lengkap berlangsung saat migration. `config/databases.example.json` adalah contoh konfigurasi/payload, bukan sumber konfigurasi runtime.
4. Pada **User**, buat pengguna bila diperlukan. Pada **Group & akses**, buat group, pilih **Anggota & akses**, tambahkan ID user, dan berikan ID database dengan tingkat **View Only** atau **View + Edit**. ID dapat dilihat pada daftar user dan koneksi.
5. Admin platform tidak otomatis dapat mengakses target. Masukkan admin ke group yang memiliki grant jika admin juga perlu menggunakan viewer atau editor. Session akan dirotasi atau dicabut sesuai perubahan izin; perbarui session atau login kembali bila diminta.
6. Pada **Database**, pilih database, schema, dan table, lalu buka tab Data, Column, Constraint, atau Index. Pagination tersedia di bagian bawah data.
7. Pada **Migration & history**, pilih database, periksa versi berikutnya, tulis SQL, dan jalankan dengan akses View + Edit. Lihat hasil dan riwayat percobaan. Tidak ada draft yang disimpan; **Salin ke editor** menyalin SQL history ke editor untuk ditinjau, bukan mengubah catatan lama.
8. Gunakan **Analisis query** untuk meninjau execution plan PostgreSQL. **Schema diff & kandidat migration** berada pada panel migration dan menghasilkan kandidat yang harus ditinjau sebelum dijalankan.

Password executor dan pencatat dienkripsi AES-256-GCM dengan nonce acak dan AAD ID koneksi. Respons hanya berisi metadata serta indikator konfigurasi password. Saat edit, password yang tidak dikirim dipertahankan; pengosongan memerlukan pilihan eksplisit. Perubahan atau penghapusan konfigurasi menginvalidasi pool terkait.

## Dokumentasi halaman

Screenshot berikut diambil dari aplikasi terbaru yang dijalankan dengan backend dan database PostgreSQL disposable, bukan gambar referensi atau respons API mock. Viewport desktop konsisten **1440 × 1000**; screenshot halaman penuh digunakan bila diperlukan. Semua nama akun dan data inventaris bersifat fiktif; password tidak ditampilkan. Database/akun sementara dibersihkan setelah pengambilan gambar. Data demo pada gambar tidak otomatis tersedia setelah setup.

### Login

![Halaman login](screenshots/login.png)

Pengguna masuk memakai username dan password yang sudah dibuat oleh bootstrap atau Platform Admin. Status login, kegagalan, dan session ditampilkan melalui teks. Login berhasil mengarahkan pengguna ke `/platform/`; logout mengakhiri session dan kembali ke halaman ini.

### Pilihan dan ringkasan akses database

![Pemilihan database dan ringkasan akses](screenshots/databases.png)

Pengguna yang memiliki grant memilih database pada panel **Database**, lalu memilih schema dan table. Tampilan menunjukkan nama database dan tingkat akses efektif. Daftar database pengguna disediakan melalui pilihan ini; aplikasi tidak memiliki halaman ringkasan database terpisah atau kartu statistik. Daftar seluruh konfigurasi tersedia bagi Platform Admin pada panel Koneksi.

### Schema dan column

![Viewer schema dan column](screenshots/columns.png)

Pengguna dengan View Only atau View + Edit memilih schema/table dan membuka tab **Column** untuk melihat urutan, nama, tipe data, nullable, default, serta informasi identity/generated bila tersedia. Pemilihan schema dan table tetap berada pada panel yang sama; viewer ini tidak menyediakan form perubahan struktur secara langsung.

### Constraint

![Constraint table](screenshots/constraints.png)

Tab **Constraint** menampilkan nama, jenis, kolom, referensi, definisi, dan status validasi yang tersedia dari metadata DBMS. Pengguna dengan akses baca dapat memeriksa constraint tanpa menjalankan SQL perubahan. Jika tidak ada constraint yang ditampilkan, aplikasi memberikan pesan kosong.

### Index

![Index table](screenshots/indexes.png)

Tab **Index** menampilkan nama, metode, unique/primary, status valid/ready, definisi, dan predicate bila tersedia. Pengguna yang boleh membaca database dapat meninjau index; perubahan index dilakukan melalui migration dengan izin edit.

### Data table

![Data table](screenshots/table-data.png)

Tab **Data** menampilkan satu halaman baris beserta nama dan tipe kolom. Pengguna View Only maupun View + Edit dapat membacanya. Nilai NULL, biner, numeric besar, dan nilai panjang ditampilkan sesuai representasi data backend; bagian panjang dapat dibuka untuk dibaca. Viewer tidak menyediakan penyuntingan sel langsung.

### Pagination data

![Pagination data table](screenshots/table-pagination.png)

Pengguna dapat memilih 25, 50, atau 100 baris per halaman lalu memakai **Sebelumnya/Berikutnya**. Rentang baris dan keterangan urutan ditampilkan sebagai teks. Screenshot menunjukkan halaman berikutnya dari dataset yang sama; primary key membantu urutan, tetapi penulisan data bersamaan tetap dapat menggeser pagination berbasis offset.

### Editor migration

![Editor SQL migration](screenshots/migration-editor.png)

Pengguna View + Edit memilih target, mengisi versi positif yang lebih besar dari versi sukses terakhir, lalu menulis SQL di editor monospace. **Jalankan migration** mengeksekusi perubahan nyata; **Analisis SQL ini** membuka panel analisis PostgreSQL tanpa langsung mengeksekusinya. Editor tidak menyimpan draft dan tidak mengedit record history yang sudah ada. Pengguna View Only tidak dapat mengeksekusi migration.

### Hasil eksekusi dan detail SQL

![Hasil eksekusi migration dan detail SQL](screenshots/migration-result.png)

Setelah eksekusi, panel yang sama menampilkan hasil berupa teks serta catatan history dengan status, versi, pelaksana, dan waktu. Pengguna yang memiliki akses baca dapat membuka **Lihat SQL**. Pengguna View + Edit juga dapat menyalin SQL ke editor sebagai bahan migration berikutnya; tindakan tersebut tidak mengubah record sebelumnya.

### Daftar migration dan history kegagalan

![History migration termasuk kegagalan](screenshots/migration-history.png)

History berisi percobaan terbaru, termasuk sukses dan gagal, dengan pagination serta tombol muat ulang. Contoh ini sengaja menjalankan INSERT dengan primary key duplikat untuk memperlihatkan status FAILED dan SQLSTATE. Versi gagal dapat digunakan kembali; retry membuat catatan percobaan baru. Aplikasi tidak memiliki daftar draft atau halaman detail terpisah; detail SQL dibuka di dalam tabel history.

### Administrasi user

![Daftar user dan form pembuatan user](screenshots/users.png)

Platform Admin dapat membuat user melalui username, password baru, dan pilihan peran admin, lalu melihat daftar berhalaman berisi status, peran, dan aksi. Tindakan **Nonaktifkan** memerlukan konfirmasi dan mencabut session terkait. Backend melindungi admin aktif terakhir agar tidak dinonaktifkan atau diturunkan perannya.

### Form edit user

![Form edit user](screenshots/user-form.png)

Platform Admin menggunakan **Edit** untuk mengubah username, peran, atau status aktif lalu menyimpan atau membatalkan perubahan. Form edit tidak meminta password baru; penggantian password tidak disediakan pada UI ini. Pengaktifan kembali user dapat dilakukan melalui pilihan **User aktif** pada form edit, dengan perlindungan backend yang sama.

### Group

![Daftar group dan form group](screenshots/groups.png)

Platform Admin membuat atau mengubah nama group, melihat daftar berhalaman, serta menghapus group dengan konfirmasi. Menghapus group membersihkan keanggotaan dan grant terkait tanpa menghapus user maupun database target. Akses database diberikan melalui group, bukan langsung kepada user.

### Anggota group dan pemberian akses

![Pengelolaan anggota dan pemberian akses database](screenshots/group-access.png)

Platform Admin memilih **Anggota & akses** untuk menambahkan/mengeluarkan user serta memberikan, mengubah, atau mencabut akses ke ID database. View Only memberi akses baca; View + Edit juga memberi hak migration dan bonus yang memerlukan edit. Kedua daftar memiliki pagination. Pengurangan hak dapat membuat session lama ditolak pada request berikutnya.

### Daftar koneksi database

![Daftar konfigurasi koneksi](screenshots/connections.png)

Platform Admin melihat metadata konfigurasi beserta target, TLS, indikator password, dan aksi **Edit**, **Periksa**, atau **Hapus**. Password tersimpan tidak dikirim kembali ke browser. Menghapus konfigurasi dan grant terkait tidak menghapus data maupun history pada database target.

### Form konfigurasi koneksi

![Form edit konfigurasi koneksi](screenshots/connection-form.png)

Platform Admin mengisi atau mengubah DBMS, host, port, nama database, executor, pencatat history, password, serta konfigurasi TLS. Saat edit, password tetap kosong pada form dan nilai lama dipertahankan kecuali penggantian atau pengosongan dipilih secara eksplisit. Form yang sama digunakan untuk koneksi baru dan edit; backend memvalidasi konfigurasi serta menolak database internal sebagai target.

### Query execution plan analysis

![Analisis execution plan PostgreSQL](screenshots/query-analysis.png)

Pengguna View + Edit pada target PostgreSQL dapat menjalankan EXPLAIN dan, untuk SELECT yang lolos kebijakan tambahan, EXPLAIN ANALYZE. Hasil menampilkan tree plan, scan/index/join, estimasi cost/rows, serta actual time/rows bila tersedia. JSON mentah dapat dibuka. ANALYZE benar-benar menjalankan SELECT; operasi ini tidak membuat history migration.

### Schema diff dan kandidat SQL

![Schema diff dan kandidat SQL migration](screenshots/schema-diff.png)

Pengguna View + Edit memasukkan schema tujuan dalam JSON, lalu membandingkannya dengan target. Panel menampilkan perbedaan, keputusan yang perlu diambil, serta kandidat SQL. **Salin kandidat ke editor** hanya memindahkan teks untuk ditinjau; SQL tidak dijalankan otomatis. Cakupan perbandingan dan perubahan yang tidak dapat dihasilkan otomatis dijelaskan pada bagian bonus.

### Tampilan mobile

![Login pada mobile](screenshots/mobile-login.png)

Login tetap berupa panel putih pada latar biru, dengan label dan tombol yang dapat digunakan pada layar kecil. Screenshot mobile diambil pada viewport 390 × 844 dengan form password kosong.

![Viewer pada mobile](screenshots/mobile-viewer.png)

Fungsi viewer tetap tersedia pada mobile. Navbar membungkus menjadi beberapa baris, pilihan database/schema/table disusun vertikal, dan tabel lebar dapat digulir horizontal tanpa memperlebar seluruh halaman. Fokus keyboard, label input, dan status berbentuk teks dipertahankan.

## Kebijakan akses dan session

- Password menggunakan PBKDF2-SHA256, 600.000 iterasi, salt acak 16 byte, hasil 32 byte; password baru 12–1024 byte.
- Identifier session acak 32 byte, opaque, hanya dikirim melalui cookie HttpOnly, SameSite=Lax. HTTPS memakai Secure dan prefix `__Host-`.
- Idle timeout 30 menit; absolute expiration 8 jam; rotation 15 menit; grace period identifier lama 30 detik.
- Login mengganti identifier pra-login dan langsung membatalkannya untuk mencegah fixation. Rotasi berkala/peningkatan privilege mempertahankan waktu pembuatan dan absolute expiration.
- Session disimpan di memori backend; restart mengharuskan login kembali. Deployment beberapa instance memerlukan sticky session. Penguncian migration dan versi keamanan user tetap berlaku lintas instance melalui database.
- Perubahan status/password/penurunan peran atau pengurangan membership/grant menaikkan versi keamanan sehingga session lama ditolak pada request berikutnya. Perubahan membership/grant secara konservatif dapat mencabut session meskipun group lain masih memberi akses yang sama. Peningkatan membership/grant memicu rotasi pada pemakaian berikutnya.
- Izin efektif adalah **tingkat tertinggi** dari seluruh group untuk database tersebut: View + Edit mengalahkan View Only. Tidak ada grant langsung ke user.
- Backend memeriksa identitas dan izin pada setiap operasi; ID pelaksana migration tidak diterima dari client. Seluruh perubahan state dilindungi CSRF serta pemeriksaan Origin/Fetch Metadata. Logout mencabut keluarga identifier termasuk alias grace.
- Admin aktif terakhir tidak dapat dinonaktifkan atau diturunkan perannya.

## Migration dan integritas history

Buka **Migration & history**, pilih target, susun/tinjau SQL, lalu jalankan. Tidak ada penyimpanan draft. View Only dapat membaca history/detail; editor dan operasi perubahan hanya untuk View + Edit. Semua SQL/data dirender sebagai teks.

Versi adalah bilangan positif yang harus **lebih besar** dari versi sukses terakhir; tidak diwajibkan berurutan tanpa celah. Nomor percobaan terpisah dari versi. Versi gagal boleh dipakai ulang; setiap retry tetap mempunyai catatan percobaan sendiri.

Untuk PostgreSQL, prosesnya:

1. Ambil advisory lock pada database target melalui koneksi pencatat khusus. Kunci tetap sama untuk seluruh ID konfigurasi yang menunjuk database fisik itu.
2. Pulihkan percobaan RUNNING tertinggal menjadi INTERRUPTED, baca versi terakhir, dan simpan percobaan baru bersama hash capability acak.
3. Jalankan SQL menggunakan akun executor dalam transaksi target.
4. Selesaikan history menggunakan capability yang hanya diketahui backend, di **transaksi yang sama** dengan SQL, lalu commit. SQL pengguna tidak menerima capability atau kredensial pencatat.
5. Jika SQL atau pencatatan gagal, rollback dahulu, kemudian catat FAILED melalui akun pencatat. Error publik berisi kode dan SQLSTATE, tidak berisi error driver mentah.

Privilege database melindungi history meskipun SQL mencoba UPDATE, DELETE, DROP, atau pergantian role. Larangan pencarian nama tabel bukan mekanisme perlindungannya. Capability satu kali dan status RUNNING juga memagari proses lama jika koneksi pengunci terputus. Jika commit/jaringan memberi hasil yang belum dapat dipastikan, API mengembalikan `outcome_unknown`; periksa history sebelum retry.

SQL maksimum 12 KiB. Cakupan PostgreSQL adalah DDL schema/table/column/constraint/index/sequence/type/view yang didukung validator serta INSERT/UPDATE/DELETE. Kontrol transaksi, stored program, pengaturan session, dan administrasi role tidak disediakan melalui editor. Batas request 16 KiB, konteks migration 30 detik, statement timeout PostgreSQL 20 detik, lock timeout SQL 3 detik. Akun dan fungsi target tetap harus tepercaya; platform tidak menjanjikan sandbox untuk seluruh efek eksternal SQL.

## Batas viewer dan representasi data

Viewer menampilkan schema, table, column, constraint, index, dan data. Identifier divalidasi dan di-quote terpisah; nilai pagination memakai parameter SQL. Ukuran halaman 1–100 (default 50), hasil data maksimum 2 MiB, timeout operasi 5 detik. Primary key dipakai untuk urutan stabil; tanpa primary key, UI menjelaskan bahwa urutan tidak dijamin. Perubahan data bersamaan tetap dapat menggeser halaman berbasis offset.

NULL memakai JSON null; integer besar, numeric, float, waktu, JSON/JSONB dan biner memakai representasi bertipe. Integer/numeric dikirim sebagai string untuk mempertahankan presisi; JSON tetap teks; bytea/BLOB memakai Base64. Waktu PostgreSQL mempertahankan perbedaan waktu tanpa zona dan instant UTC. MariaDB TIMESTAMP dibaca pada zona UTC; proyeksi waktu sebagai teks juga mempertahankan zero date. MariaDB JSON alias dapat muncul sebagai tipe teks menurut metadata DBMS.

## Bonus dan batas dukungan

**Schema diff / generation:** schema tujuan harus sudah ada. Input JSON berisi `schema`, daftar `tables`, serta `columns` dengan `name`, `type`, `nullable`. Mendukung pembuatan table dan penambahan column nullable. Perbedaan tipe/nullability, objek yang hilang, dan penambahan NOT NULL pada table existing ditampilkan untuk keputusan pengguna tanpa menebak rename/drop/backfill. Constraint, index, default, identity, dan enum tidak dibandingkan. Kandidat disalin ke editor untuk ditinjau/diubah; tidak dijalankan otomatis. Contoh tersedia pada panel diff.

**Query analysis PostgreSQL:** EXPLAIN mendukung satu SELECT/INSERT/UPDATE/DELETE/MERGE; DDL tidak didukung. ANALYZE benar-benar menjalankan SELECT dan dibatasi AST: tanpa fungsi/cast/CTE/penguncian, relasi harus qualified dan berupa table biasa yang lolos pemeriksaan metadata. View, RLS, inheritance/partition, index expression/predicate, generated column, dan tipe di luar kebijakan ditolak. UI menampilkan plan bertingkat, scan/index/join, estimated cost/rows, serta actual time/rows ketika tersedia. Batas 12 KiB, timeout layanan 10 detik, statement 8 detik, lock 1,5 detik. Read-only dan rollback bukan jaminan menghapus seluruh efek samping; gunakan target/akun/fungsi tepercaya. Tombol analisis terhubung ke SQL editor.

**MariaDB:** jalankan `sh scripts/dev-setup.sh --mariadb`, lalu daftarkan driver MariaDB, port `53307`, database `atlas_target`, dan dua akun terpisah. History berada dalam database `atlas_history_<16 digit hash nama target>` pada instance yang sama. Setup manual memerlukan DBA membuat database history, memberi ALL WITH GRANT OPTION hanya pada database history kepada akun pencatat, serta memberi hak aplikasi pada database target kepada executor. Jalankan `cmd/target-setup` dengan `TARGET_DRIVER=mariadb`, `TARGET_DATABASE_NAME` berisi target, `TARGET_EXECUTOR_ROLE=user@host`, dan DSN pencatat yang memilih database history. UI tidak memberikan query analysis PostgreSQL untuk MariaDB.

DML MariaDB hanya dijalankan jika seluruh table target menggunakan InnoDB; perubahan DML dan history sukses berada dalam satu transaksi lintas schema pada instance yang sama. DDL wajib satu statement dengan pilihan non-atomic eksplisit. DDL melakukan implicit commit sehingga tidak dijanjikan rollback/atomicity dengan history; kegagalan atau hasil meragukan diberi NEEDS_REVIEW. Percobaan NEEDS_REVIEW memblokir migration berikutnya. DBA perlu memeriksa apakah DDL benar-benar diterapkan, mengambil named lock history yang sama, lalu menyelesaikan status percobaan menjadi SUCCESS atau FAILED pada tabel privat sesuai hasil pemeriksaan; jangan memakai SQL migration pengguna untuk memperbaiki history. Backup dahulu dan pertahankan SQL/identitas percobaan. Bonus DBMS tambahan mempunyai batas transaksi DDL ini; jangan menyamakannya dengan jaminan PostgreSQL. Kompatibilitas MySQL tersedia pada driver tetapi **belum diklaim teruji pada server MySQL nyata**.

## Impor instalasi lama

`IDENTITY_FILE`/`DATABASE_CONFIG` tidak lagi menjadi sumber runtime. Untuk memindahkan instalasi lama, backup sumber dan database internal dahulu, hentikan penulisan pada aplikasi lama, lalu set `IMPORT_LEGACY_IDENTITY` ke file identity lama dan `IMPORT_LEGACY_DATABASES` ke konfigurasi target lama sebelum startup.

Impor menyimpan hash password tanpa menggantinya, memetakan ID user/group/target, dan mengimpor membership/grant dalam satu transaksi. Sumber tidak dihapus. Path sumber dan fingerprint dicatat: impor ulang sumber identik tidak menduplikasi data; sumber berubah atau konflik kredensial ditolak tanpa overwrite. Konfigurasi PostgreSQL lama harus memakai URL DSN; MySQL/MariaDB memakai DSN driver TCP. Akun pencatat untuk koneksi yang diimpor harus dilengkapi melalui dashboard dan setup history dijalankan eksplisit. Hapus environment impor setelah selesai. History `schema_migrations` lama tidak dihapus atau dianggap terlindungi otomatis. Engine menolak migration baru sampai history lama diimpor eksplisit. Gunakan `go run ./cmd/history-import` dengan environment lokal `TARGET_AUDIT_DSN`, `TARGET_LEGACY_DSN` (akun yang dapat membaca sumber lama), `TARGET_DATABASE_NAME`, `TARGET_DRIVER`, `PLATFORM_DATABASE_DSN`, `LEGACY_USER_MAP` (path JSON pemetaan ID lama ke ID internal), dan `LEGACY_TIMEZONE` (misalnya Asia/Jakarta bila timestamp lama berupa waktu lokal WIB). Pemetaan dapat ditinjau pada `platform.legacy_id_map`. Sumber harus tidak berubah selama impor. Destination history harus kosong pada impor pertama; reimport sumber identik tidak menduplikasi data, sumber berubah ditolak. Password tidak direset dan sumber history tidak dihapus.

## Pengujian

### Build, unit test, dan sintaks frontend

Perintah Go sama pada macOS/Linux dan PowerShell:

```bash
go build ./...
go vet ./...
go test ./...
```

Periksa sintaks JavaScript tanpa memasang framework atau dependency frontend:

```bash
for file in internal/dashboard/web/*.js; do node --check "$file" || exit 1; done
```

Padanan PowerShell:

```powershell
Get-ChildItem internal/dashboard/web/*.js | ForEach-Object {
    node --check $_.FullName
    if ($LASTEXITCODE -ne 0) { throw "Sintaks JavaScript gagal: $($_.Name)" }
}
```

Tanpa `TEST_PLATFORM_DSN`, test integrasi PostgreSQL dilewati secara eksplisit. Tanpa `TEST_MARIADB_ADMIN_DSN`, test MariaDB dilewati. Test alur MariaDB lengkap juga memakai fixture internal PostgreSQL, sehingga memerlukan kedua environment. Kelulusan `go test` tanpa DSN bukan bukti seluruh integrasi database dijalankan.

### PostgreSQL disposable untuk integration test

Gunakan cluster khusus test yang tidak berisi data pengguna. Contoh Docker berikut **terpisah dari service development**, menggunakan port lokal `55440`, container tanpa volume persisten, serta password acak. Contoh tidak menjalankan reset terhadap database development.

**macOS/Linux:**

```bash
export POSTGRES_PASSWORD="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
docker run --name atlas-test-postgres --rm -d \
  -p 127.0.0.1:55440:5432 \
  -e POSTGRES_PASSWORD -e POSTGRES_DB=atlas_test_control \
  postgres:17-alpine
# Ulangi hingga exit code 0 sebelum menjalankan test:
docker exec atlas-test-postgres pg_isready -U postgres -d atlas_test_control
export TEST_PLATFORM_DSN="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:55440/atlas_test_control?sslmode=disable"
go test -count=1 -v ./...
```

**PowerShell:**

```powershell
$env:POSTGRES_PASSWORD = python -c "import secrets; print(secrets.token_hex(24))"
docker run --name atlas-test-postgres --rm -d `
  -p 127.0.0.1:55440:5432 `
  -e POSTGRES_PASSWORD -e POSTGRES_DB=atlas_test_control `
  postgres:17-alpine
# Ulangi hingga exit code 0 sebelum menjalankan test:
docker exec atlas-test-postgres pg_isready -U postgres -d atlas_test_control
$env:TEST_PLATFORM_DSN = "postgres://postgres:$($env:POSTGRES_PASSWORD)@127.0.0.1:55440/atlas_test_control?sslmode=disable"
go test -count=1 -v ./...
```

Akun provisioning test harus dapat membuat database dan role. Helper hanya menerima localhost serta database kontrol bernama `atlas_test*`, membuat nama database/role unik, dan membersihkan objek yang dibuatnya setelah test selesai.

### MariaDB disposable untuk bonus

Pertahankan `TEST_PLATFORM_DSN` dari langkah sebelumnya. Jalankan container MariaDB khusus test pada port lokal `53308`.

**macOS/Linux:**

```bash
export MARIADB_ROOT_PASSWORD="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
docker run --name atlas-test-mariadb --rm -d \
  -p 127.0.0.1:53308:3306 \
  -e MARIADB_ROOT_PASSWORD -e MARIADB_ROOT_HOST=% \
  -e MARIADB_DATABASE=atlas_test_control mariadb:11.4
# Ulangi hingga exit code 0:
docker exec atlas-test-mariadb healthcheck.sh --connect --innodb_initialized
export TEST_MARIADB_ADMIN_DSN="root:$MARIADB_ROOT_PASSWORD@tcp(127.0.0.1:53308)/atlas_test_control?parseTime=true"
go test -count=1 -v ./...
```

**PowerShell:**

```powershell
$env:MARIADB_ROOT_PASSWORD = python -c "import secrets; print(secrets.token_hex(24))"
docker run --name atlas-test-mariadb --rm -d `
  -p 127.0.0.1:53308:3306 `
  -e MARIADB_ROOT_PASSWORD -e MARIADB_ROOT_HOST=% `
  -e MARIADB_DATABASE=atlas_test_control mariadb:11.4
# Ulangi hingga exit code 0:
docker exec atlas-test-mariadb healthcheck.sh --connect --innodb_initialized
$env:TEST_MARIADB_ADMIN_DSN = "root:$($env:MARIADB_ROOT_PASSWORD)@tcp(127.0.0.1:53308)/atlas_test_control?parseTime=true"
go test -count=1 -v ./...
```

Pemeriksaan tambahan saat kedua cluster test tersedia:

```bash
go test -race ./internal/auth ./internal/migration ./internal/server
go test -coverpkg=./internal/... -coverprofile=coverage.out ./...
```

Perintah Go di atas juga berlaku pada PowerShell. `coverage.out` diabaikan Git. Setelah test selesai, hentikan **hanya container test yang dibuat sendiri**:

```bash
docker stop atlas-test-postgres atlas-test-mariadb
# Jika hanya membuat PostgreSQL, hentikan atlas-test-postgres saja.
```

Flag `--rm` menghapus container test setelah berhenti. Pada bash/zsh, bersihkan environment dengan `unset TEST_PLATFORM_DSN TEST_MARIADB_ADMIN_DSN POSTGRES_PASSWORD MARIADB_ROOT_PASSWORD`. Pada PowerShell:

```powershell
Remove-Item Env:TEST_PLATFORM_DSN, Env:TEST_MARIADB_ADMIN_DSN, Env:POSTGRES_PASSWORD, Env:MARIADB_ROOT_PASSWORD -ErrorAction SilentlyContinue
```

### Cakupan dan pemeriksaan terakhir

Test mencakup session dan CSRF, repository dan handler, hak group, pemisahan database internal/target, privilege history, rollback, percobaan gagal, capability, representasi tipe data, schema diff, analisis query, serta konkurensi melalui beberapa pool/alias.

Pada pemeriksaan dokumentasi tanggal **25 Oktober 2026**, build, vet, sintaks JavaScript, seluruh suite dengan PostgreSQL disposable, dan alur lengkap MariaDB lulus. Browser Chrome dengan backend nyata juga digunakan untuk login/logout, viewer/pagination/metadata, migration sukses/gagal/history, diff, query analysis, serta operasi administrasi. Pemeriksaan mobile sebelumnya mencakup lebar 320 dan 390 px, keyboard, label, scroll tabel, dan penolakan eksekusi pengguna View Only pada UI/backend.

Screenshot dokumentasi diambil ulang dengan data inventaris fiktif pada versi frontend yang sama. Jalur Docker Compose dan PowerShell ditinjau berdasarkan konfigurasi/script, tetapi **belum dijalankan pada host dokumentasi ini** karena Docker dan PowerShell tidak tersedia. Database integrasi nyata dijalankan melalui cluster disposable lokal. Server MySQL nyata belum diuji; hasil MariaDB tidak dipakai sebagai klaim kompatibilitas MySQL.

## Teknologi dan struktur proyek

| Teknologi | Penggunaan aktual |
| --- | --- |
| Go 1.25 | HTTP server `net/http`, session, CSRF, service, dan akses database melalui `database/sql`. |
| HTML, CSS, vanilla JavaScript | Frontend yang disajikan oleh Go; tidak memakai framework frontend atau build Node.js. |
| `embed` | Menyertakan HTML, CSS, dan JavaScript dashboard ke binary Go. |
| PostgreSQL | Penyimpanan internal dan DBMS target utama. |
| MariaDB / driver MySQL | Target tambahan; kompatibilitas server MySQL belum diklaim teruji. |
| `github.com/lib/pq` | Driver PostgreSQL. |
| `github.com/go-sql-driver/mysql` | Driver MariaDB/MySQL. |
| `github.com/wasilibs/go-pgquery` | Parsing AST SQL PostgreSQL untuk validasi; dependency transitif tercatat di `go.mod`. |
| `crypto/pbkdf2`, SHA-256, AES-GCM | Hash password dan enkripsi kredensial koneksi. |
| Docker Compose dan Python 3 | Provisioning development dan generator secret lokal; bukan dependency runtime frontend. |
| Go `testing`, `httptest`, `go-sqlmock` | Unit/integration test dan pengujian handler/repository. |
| Playwright + Chrome | Pengambilan screenshot dan pemeriksaan browser pada environment dokumentasi; tidak menjadi dependency runtime proyek. |

```text
cmd/server/             Entrypoint server
cmd/target-setup/       Provisioning history pada target
cmd/history-import/     Impor history lama secara eksplisit
internal/auth/          Login, password, session, CSRF
internal/admin/         Administrasi user dan group
internal/access/        Grant dan perhitungan izin efektif
internal/database/      Konfigurasi, enkripsi, connector, dan pool
internal/platform/      Schema internal dan integrasi module
internal/viewer/        Metadata schema dan data table
internal/migration/     Eksekusi, history, validasi, dan schema diff
internal/queryanalysis/ Execution plan PostgreSQL
internal/dashboard/web/ HTML, CSS bersama, vanilla JavaScript
internal/server/        Integrasi route, login, dan test alur
internal/testutil/      Helper database disposable
scripts/                Generator environment dan init database
config/                 Contoh konfigurasi koneksi
screenshots/            Screenshot asli aplikasi untuk README
```

## Identitas kelompok

- Mata kuliah: IF3110 Pengembangan Aplikasi Berbasis Web.
- Kelompok: **K02-09**.
- Repository: [EliWidjojo/IF3110-K02-09](https://github.com/EliWidjojo/IF3110-K02-09), sesuai remote proyek.
- Nama, NIM, dan pembagian kontribusi anggota belum tersedia pada sumber proyek yang dapat diverifikasi; informasi tersebut belum dicantumkan.

Pengisian form pengumpulan, pendaftaran kelompok, dan jadwal demo perlu diverifikasi melalui kanal resmi perkuliahan. README ini tidak menyatakan langkah administratif tersebut telah dilakukan.
