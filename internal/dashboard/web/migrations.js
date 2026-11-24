"use strict";
(() => {
 const app=window.PlatformB,$=app.$;
 let databases=[],generation=0,controller=null,offset=0,candidate="",busy=false;
 const selected=()=>databases.find(d=>String(d.database_id)===$("migration-db").value);
 const editable=()=>selected()?.access_level==="View + Edit";
 const base=()=>`/api/databases/${encodeURIComponent($("migration-db").value)}/migrations`;
 function scope(){controller?.abort();controller=new AbortController();return {ticket:++generation,signal:controller.signal};}
 function permissions(){$("migration-db").disabled=busy||databases.length===0;const edit=editable()&&!busy;$("migration-sql").readOnly=!edit;$("migration-version").disabled=!edit;$("migration-run").disabled=!edit;$("diff-generate").disabled=!edit;$("diff-to-editor").disabled=!edit||!candidate;$("migration-analyze").disabled=!edit||selected()?.driver!=="postgres";$("migration-ddl-label").hidden=!["mysql","mariadb"].includes(selected()?.driver);$("migration-warning").hidden=$("migration-ddl-label").hidden;$("migration-access").textContent=selected()?`${selected().name} · ${selected().access_level}`:"Pilih database.";}
 function clear(){reviewed=null;$("migration-retry").hidden=true;$("migration-review").hidden=true;$("migration-job-id").value="";monitorGeneration++; retry=null; $("migration-job-status").textContent=""; scope();offset=0;candidate="";$("migration-history").replaceChildren();$("migration-sql").value="";$("migration-version").value="";$("migration-ddl").checked=false;$("diff-sql").textContent="";$("diff-changes").replaceChildren();operationFields();permissions();}
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
 let monitorGeneration=0,retry=null,reviewed=null;
 function operationFields(){
  const apply=$("migration-operation").value==="apply",pg=selected()?.driver==="postgres";
  $("migration-source-label").hidden=apply;
  $("migration-source").required=!apply;
  $("migration-sql").required=apply;
  $("migration-sql").closest("label").hidden=!apply;
  $("migration-batch-label").hidden=!apply||!pg;
  $("migration-batch-help").hidden=!apply||!pg||!$("migration-batch").value;
  $("migration-down-label").hidden=!apply||(pg&&!!$("migration-batch").value);
 }
 async function monitor(job,ticket){
  if(ticket!==monitorGeneration)return;
  $("migration-job-id").value=job.id;
  while(ticket===monitorGeneration && app.user){
   reviewed=job;
   $("migration-retry").hidden=!(editable() && job.submission.batch_size>0 && job.attempt_id && ["PARTIAL","FAILED"].includes(job.status));
   $("migration-review").hidden=job.status!=="QUEUED";
   $("migration-snapshot").textContent=JSON.stringify(job.submission,null,2);
   const round=job.retry_count ? ` · Retry #${job.retry_count}` : "";
   $("migration-job-status").textContent=`Job #${job.id}${round} · ${job.status} · ${job.committed_batches} batch, ${job.committed_rows} row committed ${job.error_code||""}`;
   if(!["QUEUED","RUNNING","READY"].includes(job.status)){await history(true);return;}
   await new Promise(resolve=>setTimeout(resolve,1500));
   if(ticket!==monitorGeneration)return;
   job=await app.api(job.status_url);
  }
 }
 async function execute(){
  if(!editable())throw new Error("Akses View + Edit diperlukan.");
  const path=base()+"/jobs",operation=$("migration-operation").value;
  const body={operation,version:Number($("migration-version").value)};
  if(operation==="apply"){
   body.query_sql=$("migration-sql").value;
   body.batch_size=selected()?.driver==="postgres"?Number($("migration-batch").value):0;
   body.down_sql=body.batch_size?"":$("migration-down").value;
   body.non_atomic_ddl=selected()?.driver!=="postgres"&&$("migration-ddl").checked;
  }else{body.source_job=Number($("migration-source").value);}
  const fingerprint=path+JSON.stringify(body);
  if(retry?.fingerprint!==fingerprint)retry={fingerprint,key:crypto.randomUUID()};
  busy=true;permissions();
  try{
   const ticket=++monitorGeneration;
   const job=await app.api(path,{method:"POST",headers:{"Idempotency-Key":retry.key},body});
   retry=null;
   monitor(job,ticket).catch(e=>{if(ticket===monitorGeneration)app.notice(e.message,true);});
  }finally{busy=false;permissions();}
 }
 async function diff(){if(!editable())throw new Error("Akses View + Edit diperlukan.");let input;try{input=JSON.parse($("diff-input").value);}catch{throw new Error("Schema tujuan harus berupa JSON valid.");}const request=scope();candidate="";permissions();const result=await app.api(`${base()}/diff`,{method:"POST",body:input,signal:request.signal});if(request.ticket!==generation)return;candidate=result.sql;$("diff-sql").textContent=candidate||"Tidak ada SQL tambahan yang dapat dihasilkan otomatis.";$("diff-limitations").textContent=result.limitations;app.table($("diff-changes"),[{label:"Objek",value:d=>d.object},{label:"Perbedaan",value:d=>d.kind},{label:"Keputusan",value:d=>`${d.requires_decision?"Perlu keputusan: ":""}${d.message}`}],result.differences,"Tidak ada perbedaan pada cakupan yang didukung.");permissions();}
 document.addEventListener("DOMContentLoaded",()=>{
  $("migration-monitor").addEventListener("click",()=>app.action(async()=>{
   const ticket=++monitorGeneration;reviewed=null;$("migration-review").hidden=true;$("migration-retry").hidden=true;
   const job=await app.api(base()+"/jobs/"+encodeURIComponent($("migration-job-id").value));
   await monitor(job,ticket);
  }));
  $("migration-retry").addEventListener("click",()=>app.action(async()=>{
   const job=reviewed;
   if(!job||!editable())return;
   const ticket=++monitorGeneration;
   const queued=await app.api(job.status_url+"/retries/"+(job.retry_count+1),{method:"PUT"});
   await monitor(queued,ticket);
  },$("migration-retry")));
  $("migration-approve").addEventListener("click",()=>app.action(async()=>{
   if(!reviewed||reviewed.status!=="QUEUED")return;
   const group=Number($("migration-group").value);
   if(!Number.isSafeInteger(group)||group<=0)throw new Error("Masukkan ID grup leader.");
   await app.api(reviewed.status_url+"/approvals/"+group+"?retry_count="+reviewed.retry_count,{method:"PUT"});
   app.notice("Persetujuan grup tersimpan.");
  }));
  $("migration-operation").addEventListener("change",operationFields);$("migration-batch").addEventListener("input",operationFields);
  $("migration-db").addEventListener("change",()=>{clear();app.action(()=>history(true));});$("migration-form").addEventListener("submit",event=>{event.preventDefault();app.action(execute);});
  $("history-refresh").addEventListener("click",()=>app.action(()=>history()));$("history-prev").addEventListener("click",()=>{offset=Math.max(0,offset-25);app.action(()=>history());});$("history-next").addEventListener("click",()=>{offset+=25;app.action(()=>history());});
  $("migration-analyze").addEventListener("click",()=>{if(editable())window.PlatformAnalysis.open({databaseId:$("migration-db").value,sql:$("migration-sql").value});});
  $("diff-generate").addEventListener("click",()=>app.action(diff,$("diff-generate")));$("diff-to-editor").addEventListener("click",()=>{if(editable()&&candidate){$("migration-sql").value=candidate;$("migration-sql").focus();}});
 });
 window.addEventListener("platform:databases",event=>{databases=event.detail;app.options($("migration-db"),databases,d=>d.database_id,d=>`${d.name} · ${d.access_level}`);clear();});
 window.addEventListener("platform:invalidate",()=>{databases=[];app.options($("migration-db"),[],d=>d.database_id,d=>d.name,"Perbarui akses");clear();$("migration-status").textContent="Session atau izin perlu diperiksa ulang.";});
})();
