const r=new Set(["class","style"]);function c(n){const e={},s={};for(const[t,o]of Object.entries(n))r.has(t.toLowerCase())?e[t]=o:s[t]=o;return{wrapper:e,control:s}}export{c as s};
