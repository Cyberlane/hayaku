export default { async fetch(request:Request, env:any) { return new Response(`${env.GREETING}:${new URL(request.url).pathname}`); } };
