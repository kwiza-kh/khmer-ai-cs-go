"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { Play, Pause } from "lucide-react"
import { cn } from "@/lib/utils"

interface VoiceMessageBubbleProps {
  audioSrc: string
  /** Fallback duration in seconds, used until real metadata loads. */
  duration?: number
  bubbleColor?: string
  waveColor?: string
  className?: string
  playLabel?: string
  pauseLabel?: string
}

// Deterministic pseudo-waveform: stable across SSR + re-renders (Math.random
// here would re-roll the bars on every state update and break hydration).
const WAVE_BARS = Array.from({ length: 30 }, (_, idx) => {
  const a = Math.abs(Math.sin(idx * 12.9898) * 43758.5453) % 1
  const b = Math.abs(Math.sin(idx * 78.233 + 1.7) * 12543.21) % 1
  return 4 + ((a + b) / 2) * 12
})

function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "--"
  const total = Math.round(seconds)
  if (total < 60) return `${total}s`
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`
}

export default function VoiceMessageBubble({
  audioSrc,
  duration,
  bubbleColor = "#fff",
  waveColor = "#000",
  className,
  playLabel = "Play",
  pauseLabel = "Pause",
}: VoiceMessageBubbleProps) {
  // Audio must be created client-side: `new Audio()` during render would
  // crash the Next.js prerender of this "use client" component.
  const [audio, setAudio] = React.useState<HTMLAudioElement | null>(null)
  const [isPlaying, setIsPlaying] = React.useState(false)
  const [progress, setProgress] = React.useState(0)
  const [realDuration, setRealDuration] = React.useState<number | null>(null)

  React.useEffect(() => {
    const el = new Audio(audioSrc)
    el.preload = "metadata"
    const handleTimeUpdate = () => {
      if (el.duration > 0) setProgress((el.currentTime / el.duration) * 100)
    }
    const handleLoaded = () => {
      if (Number.isFinite(el.duration) && el.duration > 0) setRealDuration(el.duration)
    }
    const handlePlay = () => setIsPlaying(true)
    const handlePause = () => setIsPlaying(false)
    const handleEnded = () => {
      setIsPlaying(false)
      setProgress(0)
    }
    el.addEventListener("timeupdate", handleTimeUpdate)
    el.addEventListener("loadedmetadata", handleLoaded)
    el.addEventListener("play", handlePlay)
    el.addEventListener("pause", handlePause)
    el.addEventListener("ended", handleEnded)
    setAudio(el)
    return () => {
      el.removeEventListener("timeupdate", handleTimeUpdate)
      el.removeEventListener("loadedmetadata", handleLoaded)
      el.removeEventListener("play", handlePlay)
      el.removeEventListener("pause", handlePause)
      el.removeEventListener("ended", handleEnded)
      el.pause()
      el.src = ""
      setAudio(null)
    }
  }, [audioSrc])

  const togglePlay = () => {
    if (!audio) return
    if (isPlaying) void audio.pause()
    else void audio.play().catch(() => setIsPlaying(false))
  }

  const shownDuration = realDuration ?? duration ?? 0

  return (
    <div
      className={cn(
        "flex items-center gap-3 p-3 rounded-xl shadow-sm",
        className
      )}
      style={{ backgroundColor: bubbleColor }}
    >
      {/* Play/Pause Button */}
      <Button
        variant="outline"
        className="p-2 rounded-full"
        onClick={togglePlay}
        aria-label={isPlaying ? pauseLabel : playLabel}
      >
        {isPlaying ? <Pause className="w-4 h-4" /> : <Play className="w-4 h-4" />}
      </Button>

      {/* Waveform */}
      <div
        className="flex-1 h-6 relative cursor-pointer"
        onClick={(e) => {
          const rect = e.currentTarget.getBoundingClientRect()
          const clickX = e.clientX - rect.left
          if (audio && audio.duration > 0) {
            audio.currentTime = Math.min(1, Math.max(0, clickX / rect.width)) * audio.duration
          }
        }}
      >
        <div className="absolute inset-0 flex justify-between items-center px-0.5">
          {WAVE_BARS.map((height, idx) => (
            <div
              key={idx}
              className="rounded-sm"
              style={{ width: 2, height: `${height}px`, backgroundColor: waveColor }}
            />
          ))}
        </div>

        {/* Progress Overlay */}
        <div
          className="absolute top-0 left-0 h-full rounded-sm"
          style={{
            width: `${progress}%`,
            backgroundColor: waveColor,
            opacity: 0.3,
          }}
        />
      </div>

      {/* Duration */}
      <span className="text-sm font-mono" style={{ color: waveColor }}>
        {formatDuration(shownDuration)}
      </span>
    </div>
  )
}
