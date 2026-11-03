-- The stt function transcribed a recording for every call that asked, so
-- two at once, two devices or a second tap, both sent its audio and spent
-- the person's key; and one the platform stopped left the recording
-- transcribing. The status cannot tell, since the app sets it itself
-- before it asks. transcribing_at is when a call of the function took the
-- recording to transcribe, null when none has it: a call takes it only
-- when no other call has, or when the one that had it is older than any
-- call runs, which the platform stopped; it is cleared as the transcript,
-- or the failure, is saved. The apps neither read nor send it, and an
-- editor who sets it by hand can do no more than delay a transcription of
-- a recording they could delete.

alter table public.recordings add column if not exists transcribing_at timestamptz;
