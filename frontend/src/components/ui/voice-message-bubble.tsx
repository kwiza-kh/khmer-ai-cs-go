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
  /** Screen-reader label for the seekable waveform. */
  seekLabel?: string
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
  seekLabel = "Seek",
}: VoiceMessageBubbleProps) {
  // Audio must be created client-side: `new Audio()` during render would crash
  // the Next.js prerender of this "use client" component. The element lives in a
  // ref: it is not render data, so keeping it in state only bought an extra
  // render on mount plus a "value cannot be modified" error on every seek.
  const audioRef = React.useRef<HTMLAudioElement | null>(null)
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
    audioRef.current = el
    return () => {
      el.removeEventListener("timeupdate", handleTimeUpdate)
      el.removeEventListener("loadedmetadata", handleLoaded)
      el.removeEventListener("play", handlePlay)
      el.removeEventListener("pause", handlePause)
      el.removeEventListener("ended", handleEnded)
      el.pause()
      el.src = ""
      audioRef.current = null
    }
  }, [audioSrc])

  const togglePlay = () => {
    const el = audioRef.current
    if (!el) return
    if (isPlaying) void el.pause()
    else void el.play().catch(() => setIsPlaying(false))
  }

  // Seek by fraction of the track — shared by pointer and keyboard so the two
  // cannot drift apart.
  const seekToFraction = (track: HTMLElement, clientX: number) => {
    const el = audioRef.current
    if (!el || !(el.duration > 0)) return
    const rect = track.getBoundingClientRect()
    el.currentTime = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width)) * el.duration
  }

  const seekBy = (seconds: number) => {
    const el = audioRef.current
    if (!el || !(el.duration > 0)) return
    el.currentTime = Math.min(el.duration, Math.max(0, el.currentTime + seconds))
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

      {/* Waveform — a button, not a div: seeking used to be pointer-only, so
          keyboard and screen-reader users could not move through the recording
          at all (the play button was the only accessible control). */}
      <button
        type="button"
        role="slider"
        className="flex-1 h-6 relative cursor-pointer appearance-none border-0 bg-transparent p-0"
        aria-label={seekLabel}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(progress)}
        onClick={(e) => seekToFraction(e.currentTarget, e.clientX)}
        onKeyDown={(e) => {
          if (e.key === "ArrowRight") { e.preventDefault(); seekBy(5) }
          else if (e.key === "ArrowLeft") { e.preventDefault(); seekBy(-5) }
          else if (e.key === "Home") { e.preventDefault(); seekBy(-Infinity) }
          else if (e.key === "End") { e.preventDefault(); seekBy(Infinity) }
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
      </button>

      {/* Duration */}
      <span className="text-sm font-mono" style={{ color: waveColor }}>
        {formatDuration(shownDuration)}
      </span>
    </div>
  )
}
