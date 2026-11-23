#ATC PROGRAM

#Jeda minimal antar pesawat dalam satu runway (menit)
JEDA_PESAWAT = 5

def ke_menit(jam):
    #Mengubah format HH:MM menjadi jumlah menit
    h, m = jam.split(":")
    return int(h) * 60 + int(m)


def ke_jam(menit):
    #Mengubah jumlah menit menjadi format HH:MM
    return f"{(menit // 60) % 24:02d}:{menit % 60:02d}"


def input_angka(teks):
    #Memastikan input berupa angka bulat
    while True:
        try:
            angka = int(input(teks))
            if angka >= 0:
                return angka
            print("Masukkan angka 0 atau lebih.")
        except ValueError:
            print("Input harus berupa angka bulat.")

def input_jam(teks):
    #Memastikan format jam benar, misalnya 07:15
    while True:
        jam = input(teks)
        try:
            h, m = jam.split(":")
            h, m = int(h), int(m)
            if 0 <= h <= 23 and 0 <= m <= 59:
                return f"{h:02d}:{m:02d}"
        except ValueError:
            pass
        print("Format jam salah. Gunakan HH:MM, contoh 07:15.")

def input_pesawat(nama_runway):
    #Mengumpulkan ID dan waktu permintaan pesawat
    print(f"INPUT {nama_runway}")
    jumlah = input_angka("Jumlah pesawat: ")
    daftar = []

    for i in range(jumlah):
        print(f"Pesawat ke-{i + 1}")
        id_pesawat = input("ID pesawat (contoh: GA123): ")
        jam_minta = input_jam("Jam permintaan (HH:MM): ")
        daftar.append((ke_menit(jam_minta), id_pesawat))

    return daftar

def proses_runway(nama_runway, daftar_pesawat):
    #Mengurutkan antrean dan menghindari jadwal yang bentrok
    # Pesawat diproses berdasarkan waktu permintaan paling awal
    daftar_pesawat.sort()
    jadwal = []
    hasil = []

    for jam_minta, id_pesawat in daftar_pesawat:
        jam_jadwal = jam_minta

        # Jika bentrok, geser jadwal sampai tersedia.
        bentrok = True
        while bentrok:
            bentrok = False
            for jam_lain, _ in jadwal:
                if abs(jam_jadwal - jam_lain) < JEDA_PESAWAT:
                    jam_jadwal = jam_lain + JEDA_PESAWAT
                    bentrok = True
                    break

        jadwal.append((jam_jadwal, id_pesawat))
        delay = jam_jadwal - jam_minta
        hasil.append((id_pesawat, jam_minta, jam_jadwal, delay))
  
    print(f"                 {nama_runway}")

    if not hasil:
        print("Belum ada pesawat yang dimasukkan.")
    else:
        for id_pesawat, jam_minta, jam_jadwal, delay in hasil:
            if delay == 0:
                status = "Tepat waktu"
            else:
                status = f"Delay {delay} menit"
            print(f"- Pesawat {id_pesawat} | Jam Permintaan: {ke_jam(jam_minta)} | Jam yang Didapat: {ke_jam(jam_jadwal)} | Status: {status}")
            print("")

    return hasil

print("           AIRPORT SCHEDULER")
print("     Sistem Antrean Landing & Take Off")

landing = input_pesawat("LANDING")
takeoff = input_pesawat("TAKE OFF")

print("")
hasil_landing = proses_runway("                        LANDING", landing)
hasil_takeoff = proses_runway("                        TAKE OFF", takeoff)

