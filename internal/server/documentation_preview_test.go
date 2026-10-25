package server

import (
 "fmt"
 "net"
 "net/http"
 "os"
 "testing"
 "time"
)

func TestDocumentationPreview(t *testing.T) {
 if os.Getenv("ATLAS_DOCUMENTATION_PREVIEW")!="1" { t.Skip("documentation fixture only") }
 f:=fixture(t)
 group:=f.grant(t,2,"View + Edit")
 status(t,f.admin.request("POST",fmt.Sprintf("/api/admin/groups/%d/members",group),`{"user_id":1}`,nil),201)
 f.admin.login("admin")
 status(t,f.admin.request("POST","/api/admin/users",`{"username":"pembaca_demo","password":"test-password-long-enough","is_platform_admin":false}`,nil),201)
 f.grant(t,3,"View Only")
 if _,err:=f.internal.DB.Exec(`UPDATE platform.users SET username='admin_demo' WHERE id=1; UPDATE platform.users SET username='editor_demo' WHERE id=2; UPDATE platform.groups SET name='Editor Inventaris' WHERE id=1; UPDATE platform.groups SET name='Pembaca Inventaris' WHERE id=2; UPDATE platform.database_connections SET name='Inventaris Demo' WHERE id=1`);err!=nil { t.Fatal(err) }
 f.admin.login("admin_demo")
 status(t,f.execute(f.admin,1,`CREATE TABLE public.produk(id BIGINT PRIMARY KEY,nama TEXT NOT NULL,kategori TEXT NOT NULL,harga NUMERIC(12,2) NOT NULL,stok INTEGER NOT NULL DEFAULT 0,CONSTRAINT harga_positif CHECK(harga>0),CONSTRAINT stok_nonnegatif CHECK(stok>=0)); CREATE INDEX produk_kategori_idx ON public.produk(kategori); INSERT INTO public.produk SELECT n,'Produk '||lpad(n::text,3,'0'),CASE WHEN n%2=0 THEN 'Alat tulis' ELSE 'Perlengkapan' END,10000+n*500,n*2 FROM generate_series(1,64) n`),200)
 listener,err:=net.Listen("tcp","127.0.0.1:8080");if err!=nil {t.Fatal(err)}
 srv:=&http.Server{Handler:f.handler,ReadHeaderTimeout:5*time.Second};defer srv.Close()
 go srv.Serve(listener)
 if err:=os.WriteFile("/tmp/atlas-docs-ready",[]byte("ready"),0600);err!=nil {t.Fatal(err)}
 for i:=0;i<1800;i++ {if _,err:=os.Stat("/tmp/atlas-docs-stop");err==nil{return};time.Sleep(time.Second)}
}
