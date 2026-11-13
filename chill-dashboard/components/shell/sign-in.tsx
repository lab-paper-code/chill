"use client"

import * as React from "react"
import { Snowflake } from "lucide-react"
import type { SignInProps } from "@/lib/client"
import Button from "@/components/custom/blocks/Button"
import { InputBlock } from "@/components/custom/blocks/InputBlock"
import { Card } from "@/components/custom/blocks/CardBlock"
import { Alert } from "@/components/custom/blocks/Alerts"
import { LoaderBlock } from "@/components/custom/blocks/LoaderBlock"

/** One token field and the target system name. */
export function SignIn({ config, configError, pending, onSubmit }: SignInProps) {
  const [token, setToken] = React.useState("")
  return (
    <main id="main" className="grid min-h-dvh place-items-center bg-background px-4">
      <Card className="w-full max-w-sm p-6">
        <div className="mb-6 flex items-center gap-2">
          <Snowflake className="size-5 text-primary" aria-hidden="true" />
          <h1 className="text-base font-semibold">CHILL</h1>
          {config && <span className="ml-auto truncate text-sm text-muted-foreground">{config.systemName}</span>}
        </div>
        {pending ? (
          <div role="status" aria-label="접속 설정 확인 중"><LoaderBlock variant="spinner" size="sm" /></div>
        ) : (
          <form className="grid gap-4" onSubmit={event => { event.preventDefault(); if (token.trim()) onSubmit(token) }}>
            <InputBlock type="password" label="접속 토큰" autoComplete="off" spellCheck={false} required value={token} onChange={event => setToken(event.target.value)} />
            {configError && <Alert type="danger" icon="XCircle" title="접속 설정 조회 실패" />}
            <Button type="submit" variant="default" disabled={!config}>접속</Button>
          </form>
        )}
      </Card>
    </main>
  )
}
