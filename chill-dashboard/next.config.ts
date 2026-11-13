import type { NextConfig } from "next"

// React needs eval() in development for call stacks; production keeps the strict policy.
const scriptSrc = process.env.NODE_ENV === "production" ? "script-src 'self' 'unsafe-inline'" : "script-src 'self' 'unsafe-inline' 'unsafe-eval'"

const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  async headers() {
    return [{source:"/:path*",headers:[
      {key:"Referrer-Policy",value:"no-referrer"},
      {key:"X-Content-Type-Options",value:"nosniff"},
      {key:"Content-Security-Policy",value:`default-src 'self'; ${scriptSrc}; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'`},
    ]}]
  },
}

export default nextConfig
