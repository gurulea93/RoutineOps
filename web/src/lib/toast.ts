// Лёгкая тост-шина без React: компоненты и axios-интерсептор зовут toast(),
// а смонтированный <Toaster/> регистрирует обработчик. Так тостить можно из
// любого места (включая api.ts), не таская контекст.

export type ToastVariant = "default" | "destructive" | "success"

export interface ToastInput {
  title?: string
  description?: string
  variant?: ToastVariant
}

type Handler = (t: ToastInput) => void

let handler: Handler | null = null

export function registerToast(h: Handler | null) {
  handler = h
}

export function toast(t: ToastInput) {
  handler?.(t)
}
