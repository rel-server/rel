'use strict'

type BeforeShutdownListener = (signalOrEvent: string) => void | Promise<void>


/**
 * System signals the app will listen to initiate shutdown.
 */
const SHUTDOWN_SIGNALS = ["uncaughtException", "SIGINT", "SIGTERM", "beforeExit"]

/**
 * Time in milliseconds to wait before forcing shutdown.
 */
const SHUTDOWN_TIMEOUT = 15000

/**
 * A queue of listener callbacks to execute before shutting
 * down the process.
 */
const shutdownListeners: BeforeShutdownListener[] = []

/**
 * Listen for signals and execute given `fn` function once.
 * @param  signals System signals to listen to.
 * @param  fn Function to execute on shutdown.
 */
const processOnce = (signals: string[], fn: BeforeShutdownListener) => {
  return signals.forEach(sig => process.once(sig, fn))
}

/**
 * Sets a forced shutdown mechanism that will exit the process after `timeout` milliseconds.
 * @param  timeout Time to wait before forcing shutdown (milliseconds)
 */
const forceExitAfter = (timeout: number): BeforeShutdownListener => () => {
  setTimeout(() => {
    // Force shutdown after timeout
    console.warn(`Could not close resources gracefully after ${timeout}ms: forcing shutdown`)
    return process.exit(1)
  }, timeout).unref()
}

/**
 * Main process shutdown handler. Will invoke every previously registered async shutdown listener
 * in the queue and exit with a code of `0`. Any `Promise` rejections from any listener will
 * be logged out as a warning, but won't prevent other callbacks from executing.
 * @param  signalOrEvent The exit signal or event name received on the process.
 */
async function shutdownHandler(signalOrEvent: string) {
  console.warn(`Shutting down: received [${signalOrEvent}] signal`)

  for (const listener of shutdownListeners) {
    try {
      await listener(signalOrEvent)
    } catch (err: any) {
      console.warn(`A shutdown handler failed before completing with: ${err?.message || err}`)
    }
  }

  return process.exit(0)
}

/**
 * Registers a new shutdown listener to be invoked before exiting
 * the main process. Listener handlers are guaranteed to be called in the order
 * they were registered.
 * @param  listener The shutdown listener to register.
 * @returns Echoes back the supplied `listener`.
 */
function beforeShutdown(listener: BeforeShutdownListener) {
  shutdownListeners.push(listener)
  return listener
}

// Register shutdown callback that kills the process after `SHUTDOWN_TIMEOUT` milliseconds
// This prevents custom shutdown handlers from hanging the process indefinitely
processOnce(SHUTDOWN_SIGNALS, forceExitAfter(SHUTDOWN_TIMEOUT))

// Register process shutdown callback
// Will listen to incoming signal events and execute all registered handlers in the stack
processOnce(SHUTDOWN_SIGNALS, shutdownHandler)

export default beforeShutdown