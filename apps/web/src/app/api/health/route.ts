export async function GET(): Promise<Response> {
  return Response.json({
    status: "ok",
    service: "paper-hub-web",
  });
}
