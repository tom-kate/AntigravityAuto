const {createApp,ref,computed,onMounted,watch} = Vue;
createApp({
  setup(){
    const form=ref({proxy:'',proxy_enabled:false,cpa_token:'',cpa_api_url:'',sms_api_url:'',sms_api_key:'',sms_service:'',sms_country:0,card_number:'',card_expiry:'',card_cvv:'',card_zip:'',headless:false,concurrency:1,port:8080});
    const loaded=ref(false),saving=ref(false),saved=ref(false);

    // Country/Service search
    const countries=ref([]),services=ref([]);
    const countrySearch=ref(''),serviceSearch=ref('');
    const showCountryList=ref(false),showServiceList=ref(false);
    const loadingCountries=ref(false),loadingServices=ref(false);

    // Price & Test
    const priceInfo=ref(null);
    const testing=ref(false),testResult=ref(null);

    const filteredCountries=computed(()=>{
      const q=countrySearch.value.toLowerCase();
      if(!q) return countries.value;
      return countries.value.filter(c=>(c.chn||'').toLowerCase().includes(q)||(c.eng||'').toLowerCase().includes(q)||String(c.id).includes(q));
    });
    const filteredServices=computed(()=>{
      const q=serviceSearch.value.toLowerCase();
      if(!q) return services.value;
      return services.value.filter(s=>s.name.toLowerCase().includes(q)||s.code.toLowerCase().includes(q));
    });
    const countryName=computed(()=>{
      const c=countries.value.find(c=>c.id===form.value.sms_country);
      return c?(c.chn||c.eng):'';
    });
    const serviceName=computed(()=>{
      const s=services.value.find(s=>s.code===form.value.sms_service);
      return s?s.name:'';
    });

    function selectCountry(c){form.value.sms_country=c.id;countrySearch.value='';showCountryList.value=false;services.value=[];form.value.sms_service='';priceInfo.value=null;}
    function selectService(s){form.value.sms_service=s.code;serviceSearch.value='';showServiceList.value=false;loadPrice();}

    async function loadCountries(){
      loadingCountries.value=true;
      try{const r=await axios.get('/api/sms-countries');countries.value=r.data||[];showCountryList.value=true;}
      catch(e){alert('加载国家列表失败: '+(e.response?.data?.error||e.message))}
      finally{loadingCountries.value=false}
    }
    async function loadServices(){
      if(!form.value.sms_country)return;
      loadingServices.value=true;
      try{const r=await axios.get('/api/sms-services?country='+form.value.sms_country);services.value=r.data||[];showServiceList.value=true;}
      catch(e){alert('加载服务列表失败: '+(e.response?.data?.error||e.message))}
      finally{loadingServices.value=false}
    }

    async function loadPrice(){
      if(!form.value.sms_country||!form.value.sms_service){priceInfo.value=null;return}
      try{const r=await axios.get('/api/sms-prices?country='+form.value.sms_country+'&service='+form.value.sms_service);priceInfo.value=r.data;}
      catch(e){priceInfo.value=null;}
    }

    async function testGetNumber(){
      testing.value=true;testResult.value=null;
      try{
        // Save config first so backend uses latest settings
        await axios.post('/api/config',form.value);
        const r=await axios.post('/api/sms-test');
        testResult.value={ok:true,msg:'成功获取: '+r.data.phone+' ('+r.data.status+')'};
      }catch(e){
        testResult.value={ok:false,msg:'失败: '+(e.response?.data?.error||e.message)};
      }finally{testing.value=false}
    }

    // Close dropdowns on click outside
    document.addEventListener('click',()=>{showCountryList.value=false;showServiceList.value=false;});

    async function loadConfig(){
      try{
        const r=await axios.get('/api/config');
        const d=r.data;
        form.value=d;
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

    return{form,loaded,saving,saved,saveConfig,
      countries,services,countrySearch,serviceSearch,
      showCountryList,showServiceList,loadingCountries,loadingServices,
      filteredCountries,filteredServices,countryName,serviceName,
      selectCountry,selectService,loadCountries,loadServices,
      priceInfo,loadPrice,testing,testResult,testGetNumber}
  }
}).mount('#app');
