const {createApp,ref,reactive,computed,onMounted,onUnmounted} = Vue;
createApp({
  setup(){
    const masters=ref([]),batches=ref([]),cpaFiles=ref([]);
    const quotas=reactive({});
    const quotaLoading=ref(false);
    const quotaLoadingSet=reactive({});
    const masterInput=ref(''),addExpiry=ref(''),addRemark=ref(''),showAddModal=ref(false);
    const editingId=ref(''),editRemark=ref(''),editExpiry=ref('');
    const subModal=ref({show:false,type:'batch',masterID:'',text:'',concurrency:1});
    const cpaModal=ref({show:false,email:'',statusText:'',labelCls:'',json:'',jsonColor:''});
    const masterDetail=ref({show:false,email:'',password:'',aux_email:'',two_fa_link:'',remark:''});
    const subDetail=ref({show:false,email:'',password:'',aux_email:'',two_fa:'',status:'',step:'',phone_bound:false});
    let timer=null,cpaTimer=null;

    function showToast(msg,ok=true){
      Swal.fire({toast:true,position:'top-end',icon:ok?'success':'error',title:msg,showConfirmButton:false,timer:2500,timerProgressBar:true,
        background:'#161b22',color:'#c9d1d9',customClass:{popup:'!text-[13px] !border !border-[#30363d] !shadow-lg'}});
    }
    const totalSubs=computed(()=>{let c=0;batches.value.forEach(b=>c+=b.accounts.length);return c});

    // Status & steps
    const SL={pending:'等待中',running:'运行中',success:'成功',failed:'失败',error:'异常',finished:'已完成'};
    const SP={starting:'启动中',login:'登录',oauth:'OAuth授权',check_cpa:'CPA检测',phone_bind:'绑定手机',delete_cpa:'删除凭证',oauth_redo:'重新授权',done:'完成',retry_wait:'重试等待',exhausted:'已耗尽'};
    function statusLabel(s){return SL[s]||s}
    function stepLabel(s){return SP[s]||s||'--'}
    function statusColor(s){return{success:'text-[#3fb950]',failed:'text-[#f85149]',error:'text-[#d29922]',running:'text-[#58a6ff]',pending:'text-[#484f58]'}[s]||'text-[#484f58]'}
    function dotClass(s){return{success:'bg-[#3fb950]',failed:'bg-[#f85149]',error:'bg-[#d29922]',running:'bg-[#58a6ff] animate-pulse',pending:'bg-[#484f58]'}[s]||'bg-[#484f58]'}
    function batchLabel(s){return SL[s]||s}
    function batchColor(s){return{pending:'text-[#484f58]',running:'text-[#58a6ff]',finished:'text-[#3fb950]'}[s]||'text-[#484f58]'}
    function batchDot(s){return{pending:'bg-[#484f58]',running:'bg-[#58a6ff] animate-pulse',finished:'bg-[#3fb950]'}[s]||'bg-[#484f58]'}
    function cnt(b,st){return b.accounts.filter(a=>a.status===st).length}
    function hasNonSuccess(b){return b.accounts.some(a=>a.status!=='success')}

    // CPA
    function parseCPAMsg(msg){try{return JSON.parse(msg)}catch(e){return null}}
    function cpaHasValidURL(f){const o=parseCPAMsg(f.status_message);if(!o||!o.error||o.error.code!==403)return false;for(const d of(o.error.details||[]))for(const l of(d.links||[]))if(l.url&&l.url.includes('accounts.google.com/signin/continue'))return true;return false;}
    const CPA_TYPES={ok:['正常','text-[#3fb950]','bg-[#3fb95015]','text-[#3fb950]'],unknown:['未知','text-[#8b949e]','bg-[#21262d]','text-[#8b949e]'],parse_err:['错误','text-[#f85149]','bg-[#f8514915]','text-[#f85149]'],phone:['未绑手机','text-[#d29922]','bg-[#d2992215]','text-[#d29922]'],dead:['账号死亡','text-[#f85149]','bg-[#f8514915]','text-[#f85149]'],bad_request:['请求错误','text-[#d29922]','bg-[#d2992215]','text-[#d29922]'],quota:['额度不足','text-[#bc8cff]','bg-[#bc8cff15]','text-[#bc8cff]'],server:['官方错误','text-[#58a6ff]','bg-[#58a6ff15]','text-[#58a6ff]'],other:['其他错误','text-[#f85149]','bg-[#f8514915]','text-[#f85149]']};
    function cpaErrorType(f){if(f.status==='ok')return'ok';if(f.status!=='error')return'ok';if(!f.status_message)return'ok';const o=parseCPAMsg(f.status_message);if(!o||!o.error)return'parse_err';const c=o.error.code;if(c===403)return cpaHasValidURL(f)?'phone':'dead';if(c===429)return'quota';if(c===400||c===404)return'bad_request';if(c>=500)return'server';return'other';}
    function cpaStatusText(f){return CPA_TYPES[cpaErrorType(f)][0]}
    function cpaLabelClass(f){const t=CPA_TYPES[cpaErrorType(f)];return t[1]+' '+t[2]}
    function showCPADetail(f){const o=parseCPAMsg(f.status_message);const t=CPA_TYPES[cpaErrorType(f)];cpaModal.value={show:true,email:f.email||f.account,statusText:t[0],labelCls:t[1]+' '+t[2],json:o?JSON.stringify(o,null,2):(f.status_message||'无'),jsonColor:t[3]};}

    // Quota
    function barColor(v){if(v>=0.6)return'bg-emerald-500';if(v>=0.3)return'bg-amber-500';return'bg-red-500'}
    function barTextColor(v){if(v>=0.6)return'text-[#3fb950]';if(v>=0.3)return'text-[#d29922]';return'text-[#f85149]'}
    function fmtReset(t){if(!t)return'';const d=new Date(t),now=new Date(),diff=Math.round((d-now)/60000);if(diff<=0)return'已重置';if(diff<60)return diff+'m';const h=Math.floor(diff/60),m=diff%60;return m>0?h+'h'+m+'m':h+'h';}
    const globalQuota=computed(()=>{const groups={};let cnt=0;for(const email in quotas){const q=quotas[email];if(!q||!q.length)continue;cnt++;for(const item of q){if(!groups[item.name])groups[item.name]={total:0,count:0};groups[item.name].total+=item.remaining_fraction;groups[item.name].count++;}}if(!cnt)return null;return Object.keys(groups).map(name=>({name,avg:groups[name].total/groups[name].count}));});
    const quotaSubCount=computed(()=>Object.keys(quotas).filter(k=>quotas[k]&&quotas[k].length).length);
    function getMasterQuotaOverview(mid){const subs=getSubsForMaster(mid);const groups={};let hasAny=false;for(const s of subs){const q=quotas[s.email.toLowerCase()];if(!q)continue;hasAny=true;for(const item of q){if(!groups[item.name])groups[item.name]={total:0,count:0};groups[item.name].total+=item.remaining_fraction;groups[item.name].count++;}}if(!hasAny)return null;return Object.keys(groups).map(name=>({name,avg:groups[name].total/groups[name].count}));}

    // Time
    function fmtExpiry(t){if(!t)return'-';return new Date(t).toLocaleDateString('zh-CN',{year:'numeric',month:'2-digit',day:'2-digit'})}
    function expiryStatus(m){if(!m.expires_at)return{label:'永久',cls:'text-[#8b949e] bg-[#21262d]'};const diff=Math.ceil((new Date(m.expires_at)-new Date())/86400000);if(diff<0)return{label:'已过期',cls:'text-[#f85149] bg-[#f8514915]'};if(diff<=3)return{label:diff+'天',cls:'text-[#f85149] bg-[#f8514915] animate-pulse'};if(diff<=7)return{label:diff+'天',cls:'text-[#d29922] bg-[#d2992215]'};return{label:diff+'天',cls:'text-[#3fb950] bg-[#3fb95015]'};}

    // Data
    function getSubsForMaster(mid){const s=[];batches.value.forEach(b=>{if(b.master_id===mid)b.accounts.forEach(a=>s.push(a))});return s}
    function getBatchesForMaster(mid){return batches.value.filter(b=>b.master_id===mid).slice().reverse()}
    function getCPAFile(email){const e=email.toLowerCase();return cpaFiles.value.find(f=>(f.account||'').toLowerCase()===e||(f.email||'').toLowerCase()===e)||null}
    async function loadMain(){try{const[mr,br]=await Promise.all([axios.get('/api/masters'),axios.get('/api/batches')]);masters.value=mr.data||[];batches.value=br.data||[]}catch(e){console.error(e)}}
    async function loadCPA(){try{const cr=await axios.get('/api/cpa-files');cpaFiles.value=cr.data||[]}catch(e){console.error(e)}}

    // Quota refresh
    async function refreshAllQuotas(){if(quotaLoading.value)return;quotaLoading.value=true;const tasks=[];for(const f of cpaFiles.value){if(f.auth_index){const e=(f.email||f.account).toLowerCase();tasks.push({email:e,authIndex:f.auth_index});}}if(!tasks.length){quotaLoading.value=false;showToast('没有可查询额度的凭证',false);return}for(const t of tasks)quotaLoadingSet[t.email]=true;let ok=0,fail=0;await Promise.all(tasks.map(async t=>{try{const r=await axios.get('/api/cpa-quota?auth_index='+encodeURIComponent(t.authIndex));if(r.data&&r.data.length){quotas[t.email]=r.data;ok++}else{fail++}}catch(e){fail++}finally{delete quotaLoadingSet[t.email]}}));quotaLoading.value=false;showToast('额度刷新: '+ok+'成功'+(fail?' / '+fail+'失败':''));}
    async function refreshSingleQuota(email){const e=email.toLowerCase();const f=getCPAFile(email);if(!f||!f.auth_index){showToast('该账号无凭证',false);return}quotaLoadingSet[e]=true;try{const r=await axios.get('/api/cpa-quota?auth_index='+encodeURIComponent(f.auth_index));if(r.data&&r.data.length){quotas[e]=r.data;showToast(email.split('@')[0]+' 额度已刷新')}else showToast('未获取到额度数据',false);}catch(e2){showToast('查询失败',false)}finally{delete quotaLoadingSet[e]}}

    // 2FA
    async function get2FA(masterId){try{const r=await axios.get('/api/master/'+masterId+'/2fa');const{code,remaining}=r.data;try{await navigator.clipboard.writeText(code)}catch(e){}Swal.fire({title:'2FA 验证码',html:`<div style="font-size:36px;font-weight:bold;letter-spacing:8px;font-family:monospace;color:#e6edf3;margin:10px 0">${code}</div><div style="font-size:12px;color:#8b949e">剩余 ${remaining} 秒 · 已复制到剪贴板</div>`,background:'#161b22',color:'#c9d1d9',showConfirmButton:false,timer:3000,timerProgressBar:true,customClass:{popup:'!border !border-[#30363d] !shadow-xl'}});}catch(e){showToast(e.response?.data?.error||'获取失败',false)}}

    // Master CRUD
    function showMasterInfo(m){masterDetail.value={show:true,email:m.email,password:m.password,aux_email:m.aux_email||'',two_fa_link:m.two_fa_link||'',remark:m.remark||'无'}}
    function copyText(text){navigator.clipboard.writeText(text).then(()=>showToast('已复制')).catch(()=>{})}
    function copyMasterFull(){const d=masterDetail.value;const t=[d.email,d.password,d.aux_email,d.two_fa_link].join('----');copyText(t)}
    function showSubInfo(a){subDetail.value={show:true,email:a.email,password:a.password,aux_email:a.aux_email||'',two_fa:a.two_fa||'',status:statusLabel(a.status),step:stepLabel(a.step),phone_bound:a.phone_bound}}
    function copySubFull(){const d=subDetail.value;const t=[d.email,d.password,d.aux_email,d.two_fa].join('---');copyText(t)}
    function parseMaster(line){const p=line.split('----');if(p.length<2)return null;return{email:p[0].trim(),password:p[1].trim(),aux_email:(p[2]||'').trim(),two_fa_link:(p[3]||'').trim()}}
    async function addMaster(){const m=parseMaster(masterInput.value);if(!m){showToast('格式错误',false);return}if(addExpiry.value)m.expires_at=new Date(addExpiry.value+'T23:59:59').toISOString();if(addRemark.value)m.remark=addRemark.value;try{await axios.post('/api/masters',m);showToast('添加成功');masterInput.value='';addExpiry.value='';addRemark.value='';showAddModal.value=false;loadMain()}catch(e){showToast(e.response?.data?.error||'添加失败',false)}}
    async function delMaster(id){if(!confirm('确认删除？'))return;try{await axios.post('/api/master/'+id+'/delete');showToast('已删除');loadMain()}catch(e){showToast(e.response?.data?.error||'删除失败',false)}}
    function startEdit(m){editingId.value=m.id;editRemark.value=m.remark||'';editExpiry.value=m.expires_at?new Date(m.expires_at).toISOString().split('T')[0]:''}
    async function saveEdit(id){const p={remark:editRemark.value};if(editExpiry.value)p.expires_at=new Date(editExpiry.value+'T23:59:59').toISOString();else p.clear_expiry=true;try{await axios.post('/api/master/'+id+'/update',p);showToast('已保存');editingId.value='';loadMain()}catch(e){showToast(e.response?.data?.error||'保存失败',false)}}
    async function toggleWeeklyLimit(m){try{await axios.post('/api/master/'+m.id+'/update',{weekly_limited:!m.weekly_limited});showToast(m.weekly_limited?'已解除周限':'已标记周限');loadMain()}catch(e){showToast('操作失败',false)}}

    // Sub modal
    function openSubModal(mid,type){subModal.value={show:true,type,masterID:mid,text:'',concurrency:1}}
    async function submitSubModal(){
      const sm=subModal.value;
      if(sm.type==='batch'){
        const accs=sm.text.trim().split('\n').filter(l=>l.trim()).map(l=>{const p=l.split('---');return{email:(p[0]||'').trim(),password:(p[1]||'').trim(),aux_email:(p[2]||'').trim(),two_fa:(p[3]||'').trim()}}).filter(a=>a.email&&a.password);
        if(!accs.length){showToast('没有有效子号',false);return}
        try{const r=await axios.post('/api/batches',{master_id:sm.masterID,accounts:accs,concurrency:sm.concurrency||1});showToast('批次已创建');sm.show=false;loadMain();
          // Auto-start
          try{await axios.post('/api/batch/'+r.data.id+'/start')}catch(e){}
        }catch(e){showToast(e.response?.data?.error||'创建失败',false)}
      } else {
        const accs=sm.text.trim().split('\n').filter(l=>l.trim()).map(l=>{const p=l.split('---');return{email:(p[0]||'').trim(),password:(p[1]||'').trim(),aux_email:(p[2]||'').trim(),two_fa:(p[3]||'').trim()}}).filter(a=>a.email&&a.password);
        if(!accs.length){showToast('没有有效子号',false);return}
        try{await axios.post('/api/master/'+sm.masterID+'/import',{accounts:accs});showToast('导入成功: '+accs.length+'个子号');sm.show=false;loadMain()}
        catch(e){showToast(e.response?.data?.error||'导入失败',false)}
      }
    }

    // Batch actions
    async function startBatch(id){try{await axios.post('/api/batch/'+id+'/start');showToast('已启动');loadMain()}catch(e){showToast(e.response?.data?.error||'启动失败',false)}}
    async function deleteBatch(id){if(!confirm('确认删除？'))return;try{await axios.post('/api/batch/'+id+'/delete');showToast('已删除');loadMain()}catch(e){showToast(e.response?.data?.error||'删除失败',false)}}
    async function setSuccess(bid,idx){try{await axios.post('/api/batch/'+bid+'/account/'+idx+'/success');showToast('已设为成功');loadMain()}catch(e){showToast(e.response?.data?.error||'操作失败',false)}}
    async function delAccount(bid,idx){if(!confirm('确认删除？'))return;try{await axios.post('/api/batch/'+bid+'/account/'+idx+'/delete');showToast('已删除');loadMain()}catch(e){showToast(e.response?.data?.error||'删除失败',false)}}

    onMounted(async()=>{await loadMain();await loadCPA();timer=setInterval(loadMain,3000);cpaTimer=setInterval(loadCPA,30000);});
    onUnmounted(()=>{clearInterval(timer);clearInterval(cpaTimer)});

    return{masters,batches,cpaFiles,quotas,quotaLoading,quotaLoadingSet,masterInput,addExpiry,addRemark,showAddModal,cpaModal,masterDetail,subDetail,totalSubs,
      editingId,editRemark,editExpiry,startEdit,saveEdit,toggleWeeklyLimit,refreshAllQuotas,refreshSingleQuota,get2FA,
      subModal,openSubModal,submitSubModal,showMasterInfo,copyText,copyMasterFull,showSubInfo,copySubFull,
      statusLabel,stepLabel,statusColor,dotClass,batchLabel,batchColor,batchDot,cnt,hasNonSuccess,
      cpaStatusText,cpaLabelClass,showCPADetail,barColor,barTextColor,fmtReset,
      getMasterQuotaOverview,globalQuota,quotaSubCount,
      fmtExpiry,expiryStatus,getSubsForMaster,getBatchesForMaster,getCPAFile,
      addMaster,delMaster,showToast,startBatch,deleteBatch,setSuccess,delAccount}
  }
}).mount('#app');
