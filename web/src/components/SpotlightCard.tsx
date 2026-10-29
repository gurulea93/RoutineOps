import React, { useRef } from 'react';
import { cn } from '@/lib/utils';

// Основан на SpotlightCard из react-bits (@react-bits/SpotlightCard-TS-TW).
// Отличия от оригинала:
//   * позиция курсора пишется в CSS-переменные через ref, а не в useState —
//     оригинал делал setState на каждый mousemove и ререндерил поддерево;
//   * убрана вшитая тёмная палитра (bg-neutral-900 и т.п.): оформление задаёт
//     вызывающая сторона, цвет блика берётся из --brand;
//   * рендерится любым тегом через `as` (нам нужен button для кликабельных плиток);
//   * блик выключается при prefers-reduced-motion (см. .spotlight в index.css).

type SpotlightCardProps<T extends React.ElementType> = {
  as?: T
  className?: string
  children?: React.ReactNode
} & Omit<React.ComponentPropsWithoutRef<T>, "as" | "className" | "children">

export default function SpotlightCard<T extends React.ElementType = "div">({
  as,
  className,
  children,
  ...rest
}: SpotlightCardProps<T>) {
  const ref = useRef<HTMLElement>(null)
  const Component = (as ?? "div") as React.ElementType

  function handleMouseMove(e: React.MouseEvent<HTMLElement>) {
    const el = ref.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    el.style.setProperty("--spot-x", `${e.clientX - rect.left}px`)
    el.style.setProperty("--spot-y", `${e.clientY - rect.top}px`)
  }

  return (
    <Component ref={ref} onMouseMove={handleMouseMove} className={cn("spotlight", className)} {...rest}>
      {children}
    </Component>
  )
}
