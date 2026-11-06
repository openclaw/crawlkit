// Offline, bounded qualification of accepting branches. This mirrors the Go
// compiler; it is not a full JSON Schema satisfiability solver or a new dialect.
const object = v => v !== null && typeof v === 'object' && !Array.isArray(v);
const fail = () => { throw Object.assign(new Error('protocol_mismatch'), {code:'protocol_mismatch'}); };
const own = (v,k) => Object.hasOwn(v,k);

export function qualifyProvider(schema, resolve) {
  let remaining=50000; const spend=()=>{if(--remaining<0)fail();};
  const alternatives = (v, depth = 0) => {
    spend();
    if (depth > 64) fail(); if (typeof v === 'boolean') return [[v]]; if (!object(v)) fail();
    const base = Object.create(null);
    for (const [k,x] of Object.entries(v)) if (!['$ref','allOf','anyOf','oneOf','if','then','else'].includes(k)) base[k]=x;
    let result = [[base]];
    const join = other => { if (result.length*other.length>64) fail(); result=result.flatMap(a=>other.map(b=>[...a,...b])); };
    if (v.$ref) join(alternatives(resolve(v.$ref),depth+1));
    for (const x of v.allOf ?? []) join(alternatives(x,depth+1));
    for (const key of ['anyOf','oneOf']) if (own(v,key)) { const branches=v[key].flatMap(x=>alternatives(x,depth+1)); if(branches.length>64)fail();join(branches); }
    if (own(v,'if')) {
      const a = alternatives({allOf:[v.if,own(v,'then')?v.then:true]},depth+1);
      const b = alternatives({allOf:[{not:v.if},own(v,'else')?v.else:true]},depth+1);
      join([...a,...b]);
    }
    return result;
  };
  const isKind = (v,kind) => kind === 'object' ? object(v) : Array.isArray(v);
  const possibleKind = (v,kind,depth=0) => {
    spend();
    if(depth>64)return true;if(typeof v==='boolean')return v;
    if(own(v,'type') && ![v.type].flat().includes(kind))return false;
    if(own(v,'const') && !isKind(v.const,kind))return false;
    if(own(v,'enum') && !v.enum.some(x=>isKind(x,kind)))return false;
    if(own(v,'not') && allKind(v.not,kind,depth+1))return false;
    return true;
  };
  const allKind = (v,kind,depth=0) => {
    spend();
    if(depth>64)return false;if(typeof v==='boolean')return v;
    if(!object(v)||!possibleKind(v,kind,depth+1))return false;
    if(v.$ref&&!allKind(resolve(v.$ref),kind,depth+1))return false;
    if(own(v,'const')||own(v,'enum'))return false;
    if(own(v,'not')&&possibleKind(v.not,kind,depth+1))return false;
    const keys=kind==='object'?['properties','required','additionalProperties','minProperties','maxProperties']:['items','minItems','maxItems','uniqueItems'];
    if(keys.some(k=>own(v,k)))return false;
    if(own(v,'allOf')&&!v.allOf.every(x=>allKind(x,kind,depth+1)))return false;
    if(own(v,'anyOf')&&!v.anyOf.some(x=>allKind(x,kind,depth+1)))return false;
    if(own(v,'oneOf')) { const universal=v.oneOf.map((x,i)=>allKind(x,kind,depth+1)?i:-1).filter(i=>i>=0);if(universal.length!==1)return false;if(v.oneOf.some((x,i)=>i!==universal[0]&&possibleKind(x,kind,depth+1)))return false; }
    if(own(v,'if')) {
      const yes=own(v,'then')?v.then:true,no=own(v,'else')?v.else:true;
      if(allKind(v.if,kind,depth+1))return allKind(yes,kind,depth+1);
      if(!possibleKind(v.if,kind,depth+1))return allKind(no,kind,depth+1);
      if(!allKind(yes,kind,depth+1)||!allKind(no,kind,depth+1))return false;
    }
    return true;
  };
  const qualify = (values, depth=0) => {
    spend();
    if(depth>64)fail();
    for(const branch of alternatives({allOf:values})) {
      if(branch.some(x=>object(x)&&(own(x,'const')||own(x,'enum'))))continue;
      const objectPossible=branch.every(x=>possibleKind(x,'object'));
      const arrayPossible=branch.every(x=>possibleKind(x,'array'));
      if(objectPossible) {
        let closed=false, empty=false;const names=new Set();
        for(const x of branch) if(object(x)) {
          if(x.maxProperties===0){closed=true;empty=true;}
          if(own(x,'additionalProperties') && x.additionalProperties!==true)closed=true;
          for(const name of Object.keys(x.properties??{}))names.add(name);
        }
        if(!closed)fail();
        if(!empty) {
          for(const name of names) {
            const schemas=[];
            for(const x of branch)if(object(x)){if(own(x.properties??{},name))schemas.push(x.properties[name]);else if(own(x,'additionalProperties'))schemas.push(x.additionalProperties);}
            qualify(schemas,depth+1);
          }
          qualify(branch.filter(x=>object(x)&&own(x,'additionalProperties')).map(x=>x.additionalProperties),depth+1);
        }
      }
      if(arrayPossible) {
        const empty=branch.some(x=>object(x)&&x.maxItems===0);
        const items=branch.filter(x=>object(x)&&own(x,'items')).map(x=>x.items);
        if(!empty){if(!items.length)fail();qualify(items,depth+1);}
      }
    }
  };
  qualify([schema]);
}
