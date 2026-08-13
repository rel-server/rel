import Docker from "dockerode"
import beforeShutdown from "./before-shutdown"

const docker = new Docker()

const PG = {
  POSTGRES_USER: "app",
  POSTGRES_PASSWORD: "app",
  POSTGRES_DB: "app",
  POSTGRES_PORT: 5432,
}

const PG_IMAGE = "postgres:17"

export let pg_container: Docker.Container | null = null
export let goserver_container: Docker.Container | null = null

async function pullImageIfNeeded(image: string) {
  const images = await docker.listImages({ filters: { reference: [image] } });
  if (images.length === 0) {
    console.log(`Pulling Docker image: ${image}...`);
    const stream = await docker.pull(image)
    await new Promise<void>((resolve, reject) => {
      docker.modem.followProgress(stream, onFinished, onProgress);
      function onFinished(err: any) {
        if (err) return reject(err);
        resolve();
      }
      function onProgress() {
        // optional: handle progress events
      }
    });
    console.log(`Image pulled.`);
  } else {
    console.log(`Image ${image} already exists locally.`);
  }
}

async function startPostgresContainer(): Promise<Docker.Container> {
  await pullImageIfNeeded(PG_IMAGE);

  pg_container = await docker.createContainer({
    Image: PG_IMAGE,
    Tty: false,
    name: "goserver-test-pg",
    // name: PG_CONTAINER_NAME,
    Env: Object.entries(PG).map(([key, val]) => `${key}=${val}`),
    HostConfig: {
      AutoRemove: true,
    },
    ExposedPorts: {
      "5432/tcp": {},
    },
    Labels: {
      "com.sales-way.test": "true",
    }
  });

  await pg_container.start();

  // const logStream = await pg_container.attach({
  //   stream: true,
  //   stdout: true,
  //   stderr: true,
  //   logs: true,
  // });

  // // Demux Docker's combined stream to process.stdout and process.stderr
  // pg_container.modem.demuxStream(logStream, process.stdout, process.stderr);

  console.log('PostgreSQL container started.');
  return pg_container;
}

async function startGoserverContainer() {
  const cwd = process.cwd()
  const infos = await pg_container?.inspect()
  const uri = `postgres://${PG.POSTGRES_USER}:${PG.POSTGRES_PASSWORD}@${infos?.NetworkSettings!.IPAddress}:${PG.POSTGRES_PORT}/${PG.POSTGRES_DB}`

  await new Promise(resolve => setTimeout(resolve, 2000))

  goserver_container = await docker.createContainer({
    name: "goserver-test-web",
    Entrypoint: ["/server/server"],
    Env: [
      `PGRST_DB_URI=${uri}`,
      `PGRST_DB_ANON_ROLE=~anonymous`,
      `PGRST_DB_SCHEMA=api`,
      `SW_ENABLE_DEBUG=true`,
      `VIRTUAL_HOST=test.localhost`
    ],
    Tty: false,
    HostConfig: {
      AutoRemove: true,
      Binds: [
        `${cwd}/../server:/server/server`,
        `${cwd}/dmut:/dmut`,
      ],
    },
    Labels: {
      "com.sales-way.test": "true",
    }
  })
  await goserver_container.start()

  const logStream = await goserver_container.attach({
    stream: true,
    stdout: true,
    stderr: true,
    logs: true,
  });

  // Demux Docker's combined stream to process.stdout and process.stderr
  goserver_container.modem.demuxStream(logStream, process.stdout, process.stderr);

  console.log("Goserver container started.")
  return goserver_container
}

export async function cleanupContainers(event: string) {
  console.log(`Cleaning up containers on ${event}...`)
  await Promise.all([
    pg_container?.stop({ t: 5 }).then(() => pg_container = null),
    goserver_container?.stop({ t: 5 }).then(() => goserver_container = null),
  ])
  await new Promise(resolve => setTimeout(resolve, 1000))
}

beforeShutdown(cleanupContainers)

export async function setupContainers() {
  pg_container = await startPostgresContainer()
  goserver_container = await startGoserverContainer()

  return {
    pg_container,
    goserver_container,
    goserver: await goserver_container.inspect(),
    pg: await pg_container.inspect(),
  }
}
