import { useEffect, useRef } from "react";

// AnalyserCanvas is a tiny `<canvas>` that
// draws the live waveform from an
// AnalyserNode. The component owns the
// requestAnimationFrame loop; the parent
// passes the AnalyserNode (or undefined
// when the engine is stopped) and a
// width / height. The component clears
// itself on every frame; a stopped
// engine leaves the canvas blank so the
// slot grid's "current step" cell stays
// highlighted without a stale waveform.

type AnalyserCanvasProps = {
  // The AnalyserNode the player engine
  // hands out via getAnalyser(). When
  // undefined (engine stopped, before
  // the first start, after a dispose)
  // the canvas is cleared.
  readonly analyser: AnalyserNode | undefined;
  // Canvas dimensions in CSS pixels.
  readonly width: number;
  readonly height: number;
};

const AnalyserCanvas = ({ analyser, width, height }: AnalyserCanvasProps) => {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    const ctx = canvas.getContext("2d");
    if (!ctx) {
      return;
    }
    let raf = 0;

    const draw = () => {
      const w = canvas.width;
      const h = canvas.height;
      ctx.clearRect(0, 0, w, h);
      if (analyser) {
        const bufferLength = analyser.fftSize;
        const data = new Uint8Array(bufferLength);
        analyser.getByteTimeDomainData(data);
        ctx.lineWidth = 2;
        ctx.strokeStyle = "rgb(99 102 241)"; // indigo-500
        ctx.beginPath();
        const sliceWidth = w / bufferLength;
        let x = 0;
        for (let i = 0; i < bufferLength; i++) {
          const v = data[i] / 128.0;
          const y = (v * h) / 2;
          if (i === 0) {
            ctx.moveTo(x, y);
          } else {
            ctx.lineTo(x, y);
          }
          x += sliceWidth;
        }
        ctx.stroke();
      }
      raf = requestAnimationFrame(draw);
    };

    raf = requestAnimationFrame(draw);
    return () => {
      cancelAnimationFrame(raf);
    };
  }, [analyser]);

  return (
    <canvas
      ref={canvasRef}
      width={width * (typeof window !== "undefined" ? window.devicePixelRatio : 1)}
      height={height * (typeof window !== "undefined" ? window.devicePixelRatio : 1)}
      style={{ width: `${width}px`, height: `${height}px` }}
      className="bg-muted/30 rounded-md"
      aria-label="Sketch waveform"
    />
  );
};

export default AnalyserCanvas;
