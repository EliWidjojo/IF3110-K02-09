"use strict";
(() => {
 const app=window.PlatformB,$=app.$;
 let databases=[],generation=0,controller=null,offset=0,candidate="",busy=false;
 const selected=()=>databases.find(d=>String(d.database_id)===$("migration-db").value);
 const editable=()=>selected()?.access_level==="View + Edit";
 const base=()=>`/api/databases/${encodeURIComponent($("migration-db").value)}/migrations`;
 function scope(){controller?.abort();controller=new AbortController();return {ticket:++generation,signal:controller.signal};}
 function permissions(){$("migration-db").disabled=busy||databases.length===0;const edit=editable()&&!busy;$("migration-sql").readOnly=!edit;$("migration-version").disabled=!edit;$("migration-run").disabled=!edit;$("diff-generate").disabled=!edit;$("diff-to-editor").disabled=!edit||!candidate;$("migration-analyze").disabled=!edit||selected()?.driver!=="postgres";$("migration-ddl-label").hidden=!["mysql","mariadb"].includes(selected()?.driver);$("migration-warning").hidden=$("migration-ddl-label").hidden;$("migration-access").textContent=selected()?`${selected().name} · ${selected().access_level}`:"Pilih database.";}
 function clear(){scope();offset=0;candidate="";$("migration-history").replaceChildren();$("migration-sql").value="";$("migration-version").value="";$("migration-ddl").checked=false;$("diff-sql").textContent="";$("diff-changes").replaceChildren();permissions();}
 async function history(resetVersion=false){
  const id=$("migration-db").value;if(!id){clear();return;}
  const request=scope();$("migration-status").textContent="Memuat history…";
  try{
   const data=await app.api(`${base()}/history?limit=25&offset=${offset}`,{signal:request.signal});if(request.ticket!==generation)return;
   if(resetVersion||!$("migration-version").value)$("migration-version").value=data.latest_version+1;
   app.table($("migration-history"),[
    {label:"Percobaan / versi",value:a=>`#${a.id} · v${a.version}`},{label:"Status",value:a=>[a.status,a.sql_state,a.error_code].filter(Boolean).join(" · ")},
    {label:"Pelaksana",value:a=>`User #${a.executed_by}`},{label:"Waktu",value:a=>new Date(a.executed_at).toLocaleString("id-ID")},
    {label:"SQL",value:a=>{const detail=app.element("details");detail.append(app.element("summary","Lihat SQL"),app.element("pre",a.query_sql));if(editable())detail.append(app.button("Salin ke editor",()=>{$("migration-sql").value=a.query_sql;$("migration-sql").focus();}));return detail;}}
   ],data.attempts,"Belum ada percobaan migration.");
   $("history-prev").disabled=offset===0;$("history-next").disabled=data.attempts.length<25;$("migration-status").textContent=`Versi sukses terakhir: ${data.latest_version}.`;
  }catch(error){if(error.name!=="AbortError"&&request.ticket===generation){$("migration-history").replaceChildren();$("migration-status").textContent=error.message;throw error;}}
 }
 async function execute(){
  if(!editable())throw new Error("Akses View + Edit diperlukan.");const id=$("migration-db").value,sql=$("migration-sql").value;
  if(new TextEncoder().encode(sql).length>12288)throw new Error("SQL maksimum 12 KiB.");
  const version=Number($("migration-version").value);if(!Number.isInteger(version)||version<1||version>2147483647)throw new Error("Versi tidak valid.");
  busy=true;permissions();$("migration-status").textContent="Menjalankan migration…";
  let message="";
  try{const result=await app.api(`${base()}/execute`,{method:"POST",body:{version,query_sql:sql,non_atomic_ddl:$("migration-ddl").checked}});message=`Percobaan #${result.attempt_id} berhasil${result.atomic?"; perubahan dan history di-commit bersama.":"; DDL tidak atomik."}`;}
  catch(error){message=error.message;app.notice(error.message,true);}
  finally{busy=false;permissions();if(id===$("migration-db").value&&app.user){offset=0;try{await history(true);}catch{}$("migration-status").textContent=message;}}
 }
 async function diff(){if(!editable())throw new Error("Akses View + Edit diperlukan.");let input;try{input=JSON.parse($("diff-input").value);}catch{throw new Error("Schema tujuan harus berupa JSON valid.");}const request=scope();candidate="";permissions();const result=await app.api(`${base()}/diff`,{method:"POST",body:input,signal:request.signal});if(request.ticket!==generation)return;candidate=result.sql;$("diff-sql").textContent=candidate||"Tidak ada SQL tambahan yang dapat dihasilkan otomatis.";$("diff-limitations").textContent=result.limitations;app.table($("diff-changes"),[{label:"Objek",value:d=>d.object},{label:"Perbedaan",value:d=>d.kind},{label:"Keputusan",value:d=>`${d.requires_decision?"Perlu keputusan: ":""}${d.message}`}],result.differences,"Tidak ada perbedaan pada cakupan yang didukung.");permissions();}
 document.addEventListener("DOMContentLoaded",()=>{
  $("migration-db").addEventListener("change",()=>{clear();app.action(()=>history(true));});$("migration-form").addEventListener("submit",event=>{event.preventDefault();app.action(execute);});
  $("history-refresh").addEventListener("click",()=>app.action(()=>history()));$("history-prev").addEventListener("click",()=>{offset=Math.max(0,offset-25);app.action(()=>history());});$("history-next").addEventListener("click",()=>{offset+=25;app.action(()=>history());});
  $("migration-analyze").addEventListener("click",()=>{if(editable())window.PlatformAnalysis.open({databaseId:$("migration-db").value,sql:$("migration-sql").value});});
  $("diff-generate").addEventListener("click",()=>app.action(diff,$("diff-generate")));$("diff-to-editor").addEventListener("click",()=>{if(editable()&&candidate){$("migration-sql").value=candidate;$("migration-sql").focus();}});
 });
 window.addEventListener("platform:databases",event=>{databases=event.detail;app.options($("migration-db"),databases,d=>d.database_id,d=>`${d.name} · ${d.access_level}`);clear();});
 window.addEventListener("platform:invalidate",()=>{databases=[];app.options($("migration-db"),[],d=>d.database_id,d=>d.name,"Perbarui akses");clear();$("migration-status").textContent="Session atau izin perlu diperiksa ulang.";});
})();
