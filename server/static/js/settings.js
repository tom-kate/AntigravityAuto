const {createApp,ref,onMounted} = Vue;
createApp({
  setup(){
    const form=ref({proxy:'',proxy_enabled:false,cpa_token:'',cpa_api_url:'',sms_token:'',sms_channel_id:'',sms_code:'',headless:false,concurrency:1,port:8080});
    const loaded=ref(false),saving=ref(false),saved=ref(false);

    async function loadConfig(){
      try{
        const r=await axios.get('/api/config');
        form.value=r.data;
        loaded.value=true;
      }catch(e){console.error(e)}
    }

    async function saveConfig(){
      saving.value=true;
      try{
        await axios.post('/api/config',form.value);
        saved.value=true;
        setTimeout(()=>{saved.value=false},2000);
      }catch(e){
        alert('保存失败: '+(e.response?.data?.error||e.message));
      }finally{saving.value=false}
    }

    onMounted(loadConfig);

    return{form,loaded,saving,saved,saveConfig}
  }
}).mount('#app');
